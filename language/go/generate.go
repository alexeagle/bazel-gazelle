/* Copyright 2018 The Bazel Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package golang

import (
	"fmt"
	"go/build"
	"log"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/language/proto"
	"github.com/bazelbuild/bazel-gazelle/pathtools"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

func (gl *goLang) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	// Extract information about proto files. We need this to exclude .pb.go
	// files and generate go_proto_library rules.
	c := args.Config
	pcMode := getProtoMode(c)

	// This is a collection of proto_library rule names that have a corresponding
	// go_proto_library rule already generated.
	goProtoRules := make(map[string]struct{})

	var protoRuleNames []string
	protoPackages := make(map[string]proto.Package)
	protoFileInfo := make(map[string]proto.FileInfo)
	for _, r := range args.OtherGen {
		if r.Kind() == "go_proto_library" {
			if proto := r.AttrString("proto"); proto != "" {
				goProtoRules[proto] = struct{}{}
			}
			if protos := r.AttrStrings("protos"); protos != nil {
				for _, proto := range protos {
					goProtoRules[proto] = struct{}{}
				}
			}

		}
		if r.Kind() != "proto_library" {
			continue
		}
		pkg := r.PrivateAttr(proto.PackageKey).(proto.Package)
		protoPackages[r.Name()] = pkg
		for name, info := range pkg.Files {
			protoFileInfo[name] = info
		}
		protoRuleNames = append(protoRuleNames, r.Name())
	}
	sort.Strings(protoRuleNames)
	var emptyProtoRuleNames []string
	for _, r := range args.OtherEmpty {
		if r.Kind() == "proto_library" {
			emptyProtoRuleNames = append(emptyProtoRuleNames, r.Name())
		}
	}

	// If proto rule generation is enabled, exclude .pb.go files that correspond
	// to any .proto files present.
	regularFiles := append([]string{}, args.RegularFiles...)
	genFiles := append([]string{}, args.GenFiles...)
	if !pcMode.ShouldIncludePregeneratedFiles() {
		keep := func(f string) bool {
			for _, suffix := range []string{".pb.go", "_grpc.pb.go"} {
				if strings.HasSuffix(f, suffix) {
					if _, ok := protoFileInfo[strings.TrimSuffix(f, suffix)+".proto"]; ok {
						return false
					}
				}
			}
			return true
		}
		filterFiles(&regularFiles, keep)
		filterFiles(&genFiles, keep)
	}

	// Split regular files into files which can determine the package name and
	// import path and other files.
	var goFiles, otherFiles []string
	for _, f := range regularFiles {
		if strings.HasSuffix(f, ".go") {
			goFiles = append(goFiles, f)
		} else {
			otherFiles = append(otherFiles, f)
		}
	}

	// Look for a subdirectory named testdata. Only treat it as data if it does
	// not contain a buildable package.
	var hasTestdata bool
	for _, sub := range args.Subdirs {
		if sub == "testdata" {
			hasTestdata = !gl.goPkgRels[path.Join(args.Rel, "testdata")]
			break
		}
	}

	// Build a set of packages from files in this directory.
	goPackageMap, goFilesWithUnknownPackage := buildPackages(c, args.Dir, args.Rel, goFiles, hasTestdata)

	// Select a package to generate rules for. If there is no package, create
	// an empty package so we can generate empty rules.
	var protoName string
	pkg, err := selectPackage(c, args.Dir, goPackageMap)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			if len(protoPackages) == 1 {
				for name, ppkg := range protoPackages {
					if _, ok := goProtoRules[":"+name]; ok {
						// if a go_proto_library rule already exists for this
						// proto package, treat it as if the proto package
						// doesn't exist.
						pkg = emptyPackage(c, args.Dir, args.Rel)
						break
					}
					pkg = &goPackage{
						name:       goProtoPackageName(ppkg),
						importPath: goProtoImportPath(c, ppkg, args.Rel),
						proto:      protoTargetFromProtoPackage(name, ppkg),
					}
					protoName = name
					break
				}
			} else {
				pkg = emptyPackage(c, args.Dir, args.Rel)
			}
		} else {
			log.Print(err)
		}
	}

	// Try to link the selected package with a proto package.
	if pkg != nil {
		if pkg.importPath == "" {
			if err := pkg.inferImportPath(c); err != nil && pkg.firstGoFile() != "" {
				inferImportPathErrorOnce.Do(func() { log.Print(err) })
			}
		}
		for _, name := range protoRuleNames {
			ppkg := protoPackages[name]
			if pkg.importPath == goProtoImportPath(c, ppkg, args.Rel) {
				protoName = name
				pkg.proto = protoTargetFromProtoPackage(name, ppkg)
				break
			}
		}
	}

	// Generate rules for proto packages. These should come before the other
	// Go rules.
	g := &generator{
		c:                   c,
		rel:                 args.Rel,
		shouldSetVisibility: args.File == nil || !args.File.HasDefaultVisibility(),
	}
	var res language.GenerateResult
	var rules []*rule.Rule
	var protoEmbed string
	for _, name := range protoRuleNames {
		if _, ok := goProtoRules[":"+name]; ok {
			// if a go_proto_library rule exists for this proto_library rule
			// already, skip creating another go_proto_library for it, assuming
			// that a different gazelle extension is responsible for
			// go_proto_library rule generation.
			continue
		}
		ppkg := protoPackages[name]
		var rs []*rule.Rule
		if name == protoName {
			protoEmbed, rs = g.generateProto(pcMode, pkg.proto, pkg.importPath)
		} else {
			target := protoTargetFromProtoPackage(name, ppkg)
			importPath := goProtoImportPath(c, ppkg, args.Rel)
			_, rs = g.generateProto(pcMode, target, importPath)
		}
		rules = append(rules, rs...)
	}
	for _, name := range emptyProtoRuleNames {
		goProtoName := strings.TrimSuffix(name, "_proto") + "_go_proto"
		res.Empty = append(res.Empty, rule.NewRule("go_proto_library", goProtoName))
	}
	if pkg != nil && pcMode == proto.PackageMode && pkg.firstGoFile() == "" {
		// In proto package mode, don't generate a go_library embedding a
		// go_proto_library unless there are actually go files.
		protoEmbed = ""
	}

	// Complete the Go package and generate rules for that.
	if pkg != nil {
		// Add files with unknown packages. This happens when there are parse
		// or I/O errors. We should keep the file in the srcs list and let the
		// compiler deal with the error.
		cgo := pkg.haveCgo()
		for _, info := range goFilesWithUnknownPackage {
			if err := pkg.addFile(c, info, cgo); err != nil {
				log.Print(err)
			}
		}

		// Process the other static files.
		for _, file := range otherFiles {
			info := otherFileInfo(filepath.Join(args.Dir, file))
			if err := pkg.addFile(c, info, cgo); err != nil {
				log.Print(err)
			}
		}

		// Process generated files. Note that generated files may have the same names
		// as static files. Bazel will use the generated files, but we will look at
		// the content of static files, assuming they will be the same.
		regularFileSet := make(map[string]bool)
		for _, f := range regularFiles {
			regularFileSet[f] = true
		}
		// Some of the generated files may have been consumed by other rules
		consumedFileSet := make(map[string]bool)
		for _, r := range args.OtherGen {
			for _, f := range r.AttrStrings("srcs") {
				consumedFileSet[f] = true
			}
			if f := r.AttrString("src"); f != "" {
				consumedFileSet[f] = true
			}
		}
		for _, f := range genFiles {
			if regularFileSet[f] || consumedFileSet[f] {
				continue
			}
			info := fileNameInfo(filepath.Join(args.Dir, f))
			if err := pkg.addFile(c, info, cgo); err != nil {
				log.Print(err)
			}
		}

		// Generate Go rules.
		if protoName == "" {
			// Empty proto rules for deletion.
			_, rs := g.generateProto(pcMode, pkg.proto, pkg.importPath)
			rules = append(rules, rs...)
		}
		lib := g.generateLib(pkg, protoEmbed)
		var libName string
		if !lib.IsEmpty(goKinds[lib.Kind()]) {
			libName = lib.Name()
		}
		rules = append(rules, lib)
		if r := g.maybeGenerateToolLib(lib, pkg); r != nil {
			rules = append(rules, r)
		}
		if r := g.maybeGenerateExtraLib(lib, pkg); r != nil {
			rules = append(rules, r)
		}
		rules = append(rules,
			g.generateBin(pkg, libName),
			g.generateTest(pkg, libName))
	}

	for _, r := range rules {
		if r.IsEmpty(goKinds[r.Kind()]) {
			res.Empty = append(res.Empty, r)
		} else {
			res.Gen = append(res.Gen, r)
			res.Imports = append(res.Imports, r.PrivateAttr(config.GazelleImportsKey))
		}
	}

	if args.File != nil || len(res.Gen) > 0 {
		gl.goPkgRels[args.Rel] = true
	} else {
		for _, sub := range args.Subdirs {
			if gl.goPkgRels[path.Join(args.Rel, sub)] {
				gl.goPkgRels[args.Rel] = true
				break
			}
		}
	}

	return res
}

func filterFiles(files *[]string, pred func(string) bool) {
	w := 0
	for r := 0; r < len(*files); r++ {
		f := (*files)[r]
		if pred(f) {
			(*files)[w] = f
			w++
		}
	}
	*files = (*files)[:w]
}

func buildPackages(c *config.Config, dir, rel string, goFiles []string, hasTestdata bool) (packageMap map[string]*goPackage, goFilesWithUnknownPackage []fileInfo) {
	// Process .go and .proto files first, since these determine the package name.
	packageMap = make(map[string]*goPackage)
	for _, f := range goFiles {
		path := filepath.Join(dir, f)
		info := goFileInfo(path, rel)
		if info.packageName == "" {
			goFilesWithUnknownPackage = append(goFilesWithUnknownPackage, info)
			continue
		}
		if info.packageName == "documentation" {
			// go/build ignores this package
			continue
		}

		if _, ok := packageMap[info.packageName]; !ok {
			packageMap[info.packageName] = &goPackage{
				name:        info.packageName,
				dir:         dir,
				rel:         rel,
				hasTestdata: hasTestdata,
			}
		}
		if err := packageMap[info.packageName].addFile(c, info, false); err != nil {
			log.Print(err)
		}
	}
	return packageMap, goFilesWithUnknownPackage
}

var inferImportPathErrorOnce sync.Once

// selectPackages selects one Go packages out of the buildable packages found
// in a directory. If multiple packages are found, it returns the package
// whose name matches the directory if such a package exists.
func selectPackage(c *config.Config, dir string, packageMap map[string]*goPackage) (*goPackage, error) {
	buildablePackages := make(map[string]*goPackage)
	for name, pkg := range packageMap {
		if pkg.isBuildable(c) {
			buildablePackages[name] = pkg
		}
	}

	if len(buildablePackages) == 0 {
		return nil, &build.NoGoError{Dir: dir}
	}

	if len(buildablePackages) == 1 {
		for _, pkg := range buildablePackages {
			return pkg, nil
		}
	}

	if pkg, ok := buildablePackages[defaultPackageName(c, dir)]; ok {
		return pkg, nil
	}

	err := &build.MultiplePackageError{Dir: dir}
	for name, pkg := range buildablePackages {
		// Add the first file for each package for the error message.
		// Error() method expects these lists to be the same length. File
		// lists must be non-empty. These lists are only created by
		// buildPackage for packages with .go files present.
		err.Packages = append(err.Packages, name)
		err.Files = append(err.Files, pkg.firstGoFile())
	}
	return nil, err
}

func emptyPackage(c *config.Config, dir, rel string) *goPackage {
	pkg := &goPackage{
		name: defaultPackageName(c, dir),
		dir:  dir,
		rel:  rel,
	}
	pkg.inferImportPath(c)
	return pkg
}

func defaultPackageName(c *config.Config, rel string) string {
	gc := getGoConfig(c)
	return pathtools.RelBaseName(rel, gc.prefix, "")
}

type generator struct {
	c                   *config.Config
	rel                 string
	shouldSetVisibility bool
}

func (g *generator) generateProto(mode proto.Mode, target protoTarget, importPath string) (string, []*rule.Rule) {
	if !mode.ShouldGenerateRules() && mode != proto.LegacyMode {
		// Don't create or delete proto rules in this mode. When proto mode is disabled,
		// there may be hand-written rules or pre-generated Go files
		return "", nil
	}

	gc := getGoConfig(g.c)
	filegroupName := legacyProtoFilegroupName
	protoName := target.name
	if protoName == "" {
		importPath := InferImportPath(g.c, g.rel)
		protoName = proto.RuleName(importPath)
	}
	goProtoName := strings.TrimSuffix(protoName, "_proto") + "_go_proto"
	visibility := g.commonVisibility(importPath)

	if mode == proto.LegacyMode {
		filegroup := rule.NewRule("filegroup", filegroupName)
		if target.sources.isEmpty() {
			return "", []*rule.Rule{filegroup}
		}
		filegroup.SetAttr("srcs", target.sources.build())
		if g.shouldSetVisibility {
			filegroup.SetAttr("visibility", visibility)
		}
		return "", []*rule.Rule{filegroup}
	}

	if target.sources.isEmpty() {
		return "", []*rule.Rule{
			rule.NewRule("filegroup", filegroupName),
			rule.NewRule("go_proto_library", goProtoName),
		}
	}

	goProtoLibrary := rule.NewRule("go_proto_library", goProtoName)
	goProtoLibrary.SetAttr("proto", ":"+protoName)
	g.setImportAttrs(goProtoLibrary, importPath)
	if target.hasServices {
		goProtoLibrary.SetAttr("compilers", gc.goGrpcCompilers)
	} else if gc.goProtoCompilersSet {
		goProtoLibrary.SetAttr("compilers", gc.goProtoCompilers)
	}
	if g.shouldSetVisibility {
		goProtoLibrary.SetAttr("visibility", visibility)
	}
	goProtoLibrary.SetPrivateAttr(config.GazelleImportsKey, target.imports.build())
	return goProtoName, []*rule.Rule{goProtoLibrary}
}

func (g *generator) generateLib(pkg *goPackage, embed string) *rule.Rule {
	goLibrary := rule.NewRule("go_library", defaultLibName)
	if !pkg.library.sources.hasGo() && embed == "" {
		return goLibrary // empty
	}
	var visibility []string
	if pkg.isCommand() {
		// Libraries made for a go_binary should not be exposed to the public.
		visibility = []string{"//visibility:private"}
	} else {
		visibility = g.commonVisibility(pkg.importPath)
	}
	g.setCommonAttrs(goLibrary, pkg.rel, visibility, pkg.library, embed)
	g.setImportAttrs(goLibrary, pkg.importPath)
	return goLibrary
}

func (g *generator) generateBin(pkg *goPackage, library string) *rule.Rule {
	name := pathtools.RelBaseName(pkg.rel, getGoConfig(g.c).prefix, g.c.RepoRoot)
	goBinary := rule.NewRule("go_binary", name)
	if !pkg.isCommand() || pkg.binary.sources.isEmpty() && library == "" {
		return goBinary // empty
	}
	visibility := g.commonVisibility(pkg.importPath)
	g.setCommonAttrs(goBinary, pkg.rel, visibility, pkg.binary, library)
	return goBinary
}

func (g *generator) generateTest(pkg *goPackage, library string) *rule.Rule {
	goTest := rule.NewRule("go_test", defaultTestName)
	if !pkg.test.sources.hasGo() {
		return goTest // empty
	}
	g.setCommonAttrs(goTest, pkg.rel, nil, pkg.test, library)
	if pkg.hasTestdata {
		goTest.SetAttr("data", rule.GlobValue{Patterns: []string{"testdata/**"}})
	}
	return goTest
}

// maybeGenerateToolLib generates a go_tool_library target equivalent to the
// go_library in the same directory. maybeGenerateToolLib returns nil for
// packages outside golang.org/x/tools and for packages that aren't known
// dependencies of nogo.
//
// HACK(#834): This is only needed by golang.org/x/tools for dependencies of
// nogo. go_tool_library should be removed when bazelbuild/rules_go#2374 is
// resolved, so these targets shouldn't be generated in other repositories.
// Generating them here automatically makes it easier to upgrade
// org_golang_x_tools.
func (g *generator) maybeGenerateToolLib(lib *rule.Rule, pkg *goPackage) *rule.Rule {
	// Check whether we should generate go_tool_library.
	gc := getGoConfig(g.c)
	if gc.prefix != "golang.org/x/tools" || gc.prefixRel != "" {
		return nil
	}
	pass := strings.TrimPrefix(pkg.importPath, "golang.org/x/tools/go/analysis/passes/")
	if pass == pkg.importPath || strings.Index(pass, "/") >= 0 {
		// Not a direct dependency of nogo, possibly an indirect dependency
		switch pkg.importPath {
		case "golang.org/x/tools/go/analysis",
			"golang.org/x/tools/go/analysis/internal/facts",
			"golang.org/x/tools/go/analysis/passes/internal/analysisutil",
			"golang.org/x/tools/go/ast/astutil",
			"golang.org/x/tools/go/ast/inspector",
			"golang.org/x/tools/go/cfg",
			"golang.org/x/tools/go/gcexportdata",
			"golang.org/x/tools/go/internal/gcimporter",
			"golang.org/x/tools/go/ssa",
			"golang.org/x/tools/go/types/objectpath",
			"golang.org/x/tools/go/types/typeutil",
			"golang.org/x/tools/internal/analysisinternal":
			// Indirect dependency
		default:
			return nil
		}
	}

	// Generate the target.
	toolLib := rule.NewRule("go_tool_library", "go_tool_library")
	var visibility []string
	if pkg.importPath == "golang.org/x/tools/go/analysis/internal/facts" {
		// Imported by nogo main. We add a visibility exception.
		visibility = []string{"//visibility:public"}
	} else {
		visibility = g.commonVisibility(pkg.importPath)
	}
	g.setCommonAttrs(toolLib, pkg.rel, visibility, pkg.library, "")
	g.setImportAttrs(toolLib, pkg.importPath)
	return toolLib
}

// maybeGenerateExtraLib generates extra equivalent library targets for
// certain protobuf libraries. These "_gen" targets depend on Well Known Types
// built with go_proto_library and are used together with go_proto_library.
// The original targets are used when proto rule generation is disabled.
func (g *generator) maybeGenerateExtraLib(lib *rule.Rule, pkg *goPackage) *rule.Rule {
	gc := getGoConfig(g.c)
	if gc.prefix != "github.com/golang/protobuf" || gc.prefixRel != "" {
		return nil
	}

	var r *rule.Rule
	switch pkg.importPath {
	case "github.com/golang/protobuf/descriptor":
		r = rule.NewRule("go_library", "go_default_library_gen")
		r.SetAttr("srcs", pkg.library.sources.buildFlat())
		r.SetAttr("importpath", pkg.importPath)
		r.SetAttr("visibility", []string{"//visibility:public"})
		r.SetAttr("deps", []string{
			"//proto:go_default_library",
			"@io_bazel_rules_go//proto/wkt:descriptor_go_proto",
			"@org_golang_google_protobuf//reflect/protodesc:go_default_library",
			"@org_golang_google_protobuf//reflect/protoreflect:go_default_library",
			"@org_golang_google_protobuf//runtime/protoimpl:go_default_library",
		})

	case "github.com/golang/protobuf/jsonpb":
		r = rule.NewRule("alias", "go_default_library_gen")
		r.SetAttr("actual", ":go_default_library")
		r.SetAttr("visibility", []string{"//visibility:public"})

	case "github.com/golang/protobuf/generator":
		r = rule.NewRule("go_library", "go_default_library_gen")
		r.SetAttr("srcs", pkg.library.sources.buildFlat())
		r.SetAttr("importpath", pkg.importPath)
		r.SetAttr("visibility", []string{"//visibility:public"})
		r.SetAttr("deps", []string{
			"//proto:go_default_library",
			"//protoc-gen-go/generator/internal/remap:go_default_library",
			"@io_bazel_rules_go//proto/wkt:compiler_plugin_go_proto",
			"@io_bazel_rules_go//proto/wkt:descriptor_go_proto",
		})

	case "github.com/golang/protobuf/ptypes":
		r = rule.NewRule("go_library", "go_default_library_gen")
		r.SetAttr("srcs", pkg.library.sources.buildFlat())
		r.SetAttr("importpath", pkg.importPath)
		r.SetAttr("visibility", []string{"//visibility:public"})
		r.SetAttr("deps", []string{
			"//proto:go_default_library",
			"@io_bazel_rules_go//proto/wkt:any_go_proto",
			"@io_bazel_rules_go//proto/wkt:duration_go_proto",
			"@io_bazel_rules_go//proto/wkt:timestamp_go_proto",
			"@org_golang_google_protobuf//reflect/protoreflect:go_default_library",
			"@org_golang_google_protobuf//reflect/protoregistry:go_default_library",
		})
	}

	return r
}

func (g *generator) setCommonAttrs(r *rule.Rule, pkgRel string, visibility []string, target goTarget, embed string) {
	if !target.sources.isEmpty() {
		r.SetAttr("srcs", target.sources.buildFlat())
	}
	if target.cgo {
		r.SetAttr("cgo", true)
	}
	if !target.clinkopts.isEmpty() {
		r.SetAttr("clinkopts", g.options(target.clinkopts.build(), pkgRel))
	}
	if !target.copts.isEmpty() {
		r.SetAttr("copts", g.options(target.copts.build(), pkgRel))
	}
	if g.shouldSetVisibility && len(visibility) > 0 {
		r.SetAttr("visibility", visibility)
	}
	if embed != "" {
		r.SetAttr("embed", []string{":" + embed})
	}
	r.SetPrivateAttr(config.GazelleImportsKey, target.imports.build())
}

func (g *generator) setImportAttrs(r *rule.Rule, importPath string) {
	gc := getGoConfig(g.c)
	r.SetAttr("importpath", importPath)

	// Set importpath_aliases if we need minimal module compatibility.
	// If a package is part of a module with a v2+ semantic import version
	// suffix, packages that are not part of modules may import it without
	// the suffix.
	if gc.goRepositoryMode && gc.moduleMode && pathtools.HasPrefix(importPath, gc.prefix) && gc.prefixRel == "" {
		if mmcImportPath := pathWithoutSemver(importPath); mmcImportPath != "" {
			r.SetAttr("importpath_aliases", []string{mmcImportPath})
		}
	}

	if gc.importMapPrefix != "" {
		fromPrefixRel := pathtools.TrimPrefix(g.rel, gc.importMapPrefixRel)
		importMap := path.Join(gc.importMapPrefix, fromPrefixRel)
		if importMap != importPath {
			r.SetAttr("importmap", importMap)
		}
	}
}

func (g *generator) commonVisibility(importPath string) []string {
	// If the Bazel package name (rel) contains "internal", add visibility for
	// subpackages of the parent.
	// If the import path contains "internal" but rel does not, this is
	// probably an internal submodule. Add visibility for all subpackages.
	relIndex := pathtools.Index(g.rel, "internal")
	importIndex := pathtools.Index(importPath, "internal")
	visibility := getGoConfig(g.c).goVisibility
	if relIndex >= 0 {
		parent := strings.TrimSuffix(g.rel[:relIndex], "/")
		visibility = append(visibility, fmt.Sprintf("//%s:__subpackages__", parent))
	} else if importIndex >= 0 {
		visibility = append(visibility, "//:__subpackages__")
	} else {
		return []string{"//visibility:public"}
	}

	// Add visibility for any submodules that have the internal parent as
	// a prefix of their module path.
	if importIndex >= 0 {
		gc := getGoConfig(g.c)
		internalRoot := strings.TrimSuffix(importPath[:importIndex], "/")
		for _, m := range gc.submodules {
			if strings.HasPrefix(m.modulePath, internalRoot) {
				visibility = append(visibility, fmt.Sprintf("@%s//:__subpackages__", m.repoName))
			}
		}
	}

	return visibility
}

var (
	// shortOptPrefixes are strings that come at the beginning of an option
	// argument that includes a path, e.g., -Ifoo/bar.
	shortOptPrefixes = []string{"-I", "-L", "-F"}

	// longOptPrefixes are separate arguments that come before a path argument,
	// e.g., -iquote foo/bar.
	longOptPrefixes = []string{"-I", "-L", "-F", "-iquote", "-isystem"}
)

// options transforms package-relative paths in cgo options into repository-
// root-relative paths that Bazel can understand. For example, if a cgo file
// in //foo declares an include flag in its copts: "-Ibar", this method
// will transform that flag into "-Ifoo/bar".
func (g *generator) options(opts rule.PlatformStrings, pkgRel string) rule.PlatformStrings {
	fixPath := func(opt string) string {
		if strings.HasPrefix(opt, "/") {
			return opt
		}
		return path.Clean(path.Join(pkgRel, opt))
	}

	fixGroups := func(groups []string) ([]string, error) {
		fixedGroups := make([]string, len(groups))
		for i, group := range groups {
			opts := strings.Split(group, optSeparator)
			fixedOpts := make([]string, len(opts))
			isPath := false
			for j, opt := range opts {
				if isPath {
					opt = fixPath(opt)
					isPath = false
					goto next
				}

				for _, short := range shortOptPrefixes {
					if strings.HasPrefix(opt, short) && len(opt) > len(short) {
						opt = short + fixPath(opt[len(short):])
						goto next
					}
				}

				for _, long := range longOptPrefixes {
					if opt == long {
						isPath = true
						goto next
					}
				}

			next:
				fixedOpts[j] = escapeOption(opt)
			}
			fixedGroups[i] = strings.Join(fixedOpts, " ")
		}

		return fixedGroups, nil
	}

	opts, errs := opts.MapSlice(fixGroups)
	if errs != nil {
		log.Panicf("unexpected error when transforming options with pkg %q: %v", pkgRel, errs)
	}
	return opts
}

func escapeOption(opt string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`"`, `\"`,
		` `, `\ `,
		"\t", "\\\t",
		"\n", "\\\n",
		"\r", "\\\r",
	).Replace(opt)
}
