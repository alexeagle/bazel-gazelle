// Package main is a sample "driver" for the gazelle rosetta plugin. It is used
// to validate the functionality of the rosetta plugin but can also be used as a
// template for implementing a language plugin in another programming language.
package main

import (
	"bufio"
	"fmt"
	"os"

	pb "github.com/bazelbuild/bazel-gazelle/language/rosetta/proto"
	"github.com/gogo/protobuf/jsonpb"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(os.Stderr, "\n\nline: %s\n\n", line)
		fmt.Fprintf(os.Stderr, "\n\n multiline: %s\n\n", line)

		// https://godoc.org/github.com/golang/protobuf/jsonpb#UnmarshalString
		var msg *pb.Request
		if err := jsonpb.UnmarshalString(line, msg); err != nil {
			fmt.Fprintf(os.Stderr, "Unable to unmarshal: %v\nData:\n%s\n", err, line)
		}
		fmt.Fprintf(os.Stderr, "\n\n multiline3: %q\n\n", line)
		fmt.Fprintf(os.Stderr, "\n\nreceived1: %v\n\n", msg)
		if err := jsonpb.UnmarshalString(line, msg); err != nil {
			fmt.Fprintf(os.Stderr, "Unable to unmarshal: %v\nData:\n%s\n", err, line)
		}
		fmt.Fprintf(os.Stderr, "\n\nreceived2: %v\n\n", msg)

		fmt.Fprintf(os.Stderr, "Finished loop\n")
	}
	//fmt.Printf("Hello world from the internal process\n")
	// r := rule.NewRule("filegroup", "all_files")
	// srcs := make([]string, 0, len(args.Subdirs)+len(args.RegularFiles))
	// for _, f := range args.RegularFiles {
	// 	srcs = append(srcs, f)
	// }
	// for _, f := range args.Subdirs {
	// 	pkg := path.Join(args.Rel, f)
	// 	srcs = append(srcs, "//"+pkg+":all_files")
	// }
	// r.SetAttr("srcs", srcs)
	// r.SetAttr("testonly", true)
	// if args.File == nil || !args.File.HasDefaultVisibility() {
	// 	r.SetAttr("visibility", []string{"//visibility:public"})
	// }
	// //write to subprocess:
	// fmt.printf(marshal(language.GenerateResult{
	// 	Gen:     []*rule.Rule{r},
	// 	Imports: []interface{}{nil},
	// })
	m := &pb.Response{

		//GenerateRules: {},
	}
	_ = m
}
