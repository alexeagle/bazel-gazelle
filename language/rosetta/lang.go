package rosetta

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/bazelbuild/rules_go/go/tools/bazel"
	"github.com/golang/protobuf/jsonpb"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"

	pb "github.com/bazelbuild/bazel-gazelle/language/rosetta/proto"
)

const rosettaName = "rosetta"

type rosettaLang struct {
	cmd       *exec.Cmd
	marshaler *jsonpb.Marshaler
	stdin     io.WriteCloser
	stdout    io.ReadCloser
}

// NewLanguage is a thing
func NewLanguage() language.Language {
	bin, ok := bazel.FindBinary("language/rosetta/internal/testdriver", "testdriver")
	if !ok {
		panic(fmt.Sprintf("Unable to find binary: %v", bin))
	}
	cmd := exec.Command(bin)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		panic(fmt.Sprintf("Unable to make a StdinPipe: %v", err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(fmt.Sprintf("Unable to make a StdoutPipe: %v", err))
	}
	if err := cmd.Start(); err != nil {
		panic(fmt.Sprintf("Unable to start: %v", err))
	}

	return &rosettaLang{
		cmd: cmd,
		// https://godoc.org/github.com/golang/protobuf/jsonpb#Marshaler
		marshaler: &jsonpb.Marshaler{
			// OrigName specifies whether to use the original protobuf name for fields.
			OrigName: false,

			// EnumsAsInts specifies whether to render enum values as integers,
			// as opposed to string values.
			EnumsAsInts: false,

			// EmitDefaults specifies whether to render fields with zero values.
			EmitDefaults: true,

			// Indent controls whether the output is compact or not.
			// If empty, the output is compact JSON. Otherwise, every JSON object
			// entry and JSON array value will be on its own line.
			// Each line will be preceded by repeated copies of Indent, where the
			// number of copies is the current indentation depth.
			Indent: "",
		},
		stdin:  stdin,
		stdout: stdout,
	}
}

func (*rosettaLang) Name() string { return rosettaName }

func (*rosettaLang) RegisterFlags(fs *flag.FlagSet, cmd string, c *config.Config) {}

func (*rosettaLang) CheckFlags(fs *flag.FlagSet, c *config.Config) error { return nil }

func (*rosettaLang) KnownDirectives() []string { return nil }

func (*rosettaLang) Configure(c *config.Config, rel string, f *rule.File) {}

func (*rosettaLang) Kinds() map[string]rule.KindInfo {
	return kinds
}

func (*rosettaLang) Loads() []rule.LoadInfo { return nil }

func (*rosettaLang) Fix(c *config.Config, f *rule.File) {}

func (*rosettaLang) Imports(c *config.Config, r *rule.Rule, f *rule.File) []resolve.ImportSpec {
	return nil
}

func (*rosettaLang) Embeds(r *rule.Rule, from label.Label) []label.Label { return nil }

func (*rosettaLang) Resolve(c *config.Config, ix *resolve.RuleIndex, rc *repo.RemoteCache, r *rule.Rule, imports interface{}, from label.Label) {
}

var kinds = map[string]rule.KindInfo{
	"filegroup": {
		NonEmptyAttrs:  map[string]bool{"srcs": true, "deps": true},
		MergeableAttrs: map[string]bool{"srcs": true},
	},
}

func (r *rosettaLang) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	m := &pb.Request{
		Function: &pb.Request_GenerateRules{
			GenerateRules: &pb.GenerateRulesRequest{
				GenerateArgs: &pb.GenerateArgs{
					Dir: args.Dir,
					//
				},
			},
		},
	}
	if err := r.marshaler.Marshal(r.stdin, m); err != nil {
		panic(fmt.Sprintf("err marshalling: %v", err))
	}
	time.Sleep(1 * time.Second)

	/*
		r := rule.NewRule("filegroup", "all_files")
		srcs := make([]string, 0, len(args.Subdirs)+len(args.RegularFiles))
		for _, f := range args.RegularFiles {
			srcs = append(srcs, f)
		}
		for _, f := range args.Subdirs {
			pkg := path.Join(args.Rel, f)
			srcs = append(srcs, "//"+pkg+":all_files")
		}
		r.SetAttr("srcs", srcs)
		r.SetAttr("testonly", true)
		if args.File == nil || !args.File.HasDefaultVisibility() {
			r.SetAttr("visibility", []string{"//visibility:public"})
		}
	*/
	return language.GenerateResult{
		//Gen:     []*rule.Rule{r},
		Imports: []interface{}{nil},
	}
}
