package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Before running this:
// - Go clone repos of interest in `./repos`.
// - Maybe do `(cd repos && for x in *; do (cd $x && git fetch && git checkout origin/master); done)` to get everything up-to-date.

// Tricky todos:
// - If any of your entrypoint repos are also mid-chart (i.e. other entrypoint repos depend on them, either directly or indirectly), we get an anonymous version of the entrypoint one.  May require some UI tweakery.
// - Sometimes people make replace directives in their mod files that points to paths that aren't there.  (I'm looking at you, lotus.  There's a submodule.  Very creative.)  This makes go mod error.  That error is currently not raised very readably.

func main() {
	var relationships []Relationship
	withTimeLog("gathering mod data", func() {
		// FIMXE: actually list directories instead of hardcoding this, lol
		relationships = append(relationships, execGoModGraph("repos/go-ipfs")...)
		relationships = append(relationships, execGoModGraph("repos/lotus")...)
	})
	//fmt.Printf("%v\n", relationships)

	var pg ProcessedGraph
	withTimeLog("processing graph", func() {
		// FIMXE: actually take CLI args instead of hardcoding this, lol
		pg = walkies("github.com/ipld/go-ipld-prime", relationships)
	})

	withTimeLog("emitting dot", func() {
		emit(os.Stdout, pg)
	})
}

var cwd string

func init() {
	var err error
	cwd, err = os.Getwd()
	if err != nil {
		panic(err)
	}
}

type ModuleName string
type VersionName string
type ModuleAndVersion struct {
	Module  ModuleName
	Version VersionName
}
type Relationship struct {
	Downstream ModuleAndVersion // aka consumer
	Upstream   ModuleAndVersion // aka dependency
}

// ProcessedGraph remembers nodes that need grouping, so we can emit subgraphs in the dotfile.
type ProcessedGraph struct {
	Subgraphs     map[ModuleName]*Subgraph // subgraphs.  somewhat redundant, can be re-derived from Relationships value.
	Relationships []Relationship           // edges.  not grouped by subgraph.  (they're basically always crossing subgraphs.)
	// FUTURE: this should probably start accumulating some colorization cues too?  mostly lines will match nodes, unclear yet where I want to track that.
}

// Subgraph ropes together info about nodes in a module.
// Has an implied ModuleName, because that's a map key above this.
type Subgraph struct {
	Contains map[ModuleAndVersion]struct{} // this value might be come colorization cue data later or something?
}

func ParseModuleAndVersion(s string) ModuleAndVersion {
	ss := strings.Split(s, "@")
	switch len(ss) {
	case 2:
		return ModuleAndVersion{ModuleName(ss[0]), VersionName(ss[1])}
	case 1:
		return ModuleAndVersion{ModuleName(ss[0]), "tip"}
	default:
		panic("too many @")
	}
}
func (mv ModuleAndVersion) String() string {
	return fmt.Sprintf("%s@%s", mv.Module, mv.Version)
}

func execGoModGraph(relcwd string) []Relationship {
	cmd := exec.Command("go", "mod", "graph")
	cmd.Dir = filepath.Join(cwd, relcwd)
	out, err := cmd.Output()
	if err != nil {
		panic(err)
	}

	var result []Relationship
	lines := bytes.Split(out, []byte{'\n'})
	for _, line := range lines {
		hunks := bytes.Split(line, []byte{' '})
		if len(hunks) != 2 {
			continue
		}
		result = append(result, Relationship{
			Downstream: ParseModuleAndVersion(string(hunks[0])),
			Upstream:   ParseModuleAndVersion(string(hunks[1])),
		})
	}
	return result
}

// build a ProcessedGraph, containing only those modules that are eventually depending on the focus.
func walkies(focus ModuleName, relationships []Relationship) ProcessedGraph {
	// Dump everything in a map for fast lookup.  Filter comes later.
	var edgesByUpstreamModule = make(map[ModuleName][]Relationship)
	for _, edge := range relationships {
		edgesByUpstreamModule[edge.Upstream.Module] = append(edgesByUpstreamModule[edge.Upstream.Module], edge)
	}

	// Start from focus and just flood out.  Goes depth first.
	//  Recursive function, captures pg and accumulates results there.
	//  Some of the the values in edgesByUpstreamModule will never be touched; this is the filter.
	pg := ProcessedGraph{
		Subgraphs: make(map[ModuleName]*Subgraph, len(edgesByUpstreamModule)),
	}
	pg.flood(edgesByUpstreamModule, focus)
	return pg
}

func (pg *ProcessedGraph) flood(edgesByUpstreamModule map[ModuleName][]Relationship, module ModuleName) {
	if _, alreadyDone := pg.Subgraphs[module]; alreadyDone {
		return
	}
	pg.Subgraphs[module] = &Subgraph{
		Contains: make(map[ModuleAndVersion]struct{}),
	}
	for _, edge := range edgesByUpstreamModule[module] {
		// FIXME this is actually too inclusive somewhere.  I probably usually don't want to draw versions of a module that don't link to the focus.  It's almost interesting... but not quite.

		// Append all relationships we're seeing.
		pg.Relationships = append(pg.Relationships, edge)

		// Mark module+version pairs we into their subgraphs by module.
		pg.Subgraphs[module].Contains[edge.Upstream] = struct{}{}

		// Recurse to each module that's downstream.
		//  They may have been visited before; this'll noop if so.
		pg.flood(edgesByUpstreamModule, edge.Downstream.Module)
	}
}

func emit(w io.Writer, pg ProcessedGraph) {
	fmt.Fprintf(w, `
digraph G {
    node [penwidth=2 fontsize=10 shape=rectangle];
    edge [tailport=e penwidth=2];
    compound=true;
    rankdir=LR;
    ranksep="2.5";
    quantum="0.5";
`)
	// Future: may want to sort these or make them stable by keeping insert order (sigh).  graphviz does seem to vary things a bit based on input order.

	// Two ways to go about ranking: "rank=same" in all subgraphs, and "newrank=true" globally; or, "newrank=false" globally (so subgraphs do their own rank), and assign ranks explicitly in subgraphs.
	// The latter lets us sort things by version, so let's do that.

	for moduleName, subgraph := range pg.Subgraphs {
		fmt.Fprintf(w, `subgraph "cluster_%s" {`+"\n", moduleName)
		fmt.Fprintf(w, `label="%s";`+"\n", moduleName)
		rank := 0
		for node := range subgraph.Contains {
			fmt.Fprintf(w, `"%s" [label="%s" rank=%d];`+"\n", node, node.Version, rank)
			rank++ // Future: this would be better if we sorted.
		}
		fmt.Fprintf(w, `}`+"\n")
	}
	for _, edge := range pg.Relationships {
		fmt.Fprintf(w, `"%s" -> "%s"`+"\n", edge.Downstream, edge.Upstream)
	}
	fmt.Fprintf(w, `}`+"\n")
}

func withTimeLog(label string, fn func()) {
	fmt.Fprintf(os.Stderr, "%s...\n", label)
	start := time.Now()
	defer func() {
		fmt.Fprintf(os.Stderr, "%s completed in %dms\n", label, time.Now().Sub(start)/time.Millisecond)
	}()
	fn()
}
