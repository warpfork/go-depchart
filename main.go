package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Before running this:
// - Go clone repos of interest in `./repos`.
// - Maybe do `(cd repos && for x in *; do (cd $x && git fetch && git checkout origin/master); done)` to get everything up-to-date.

// Tricky todos:
// - Sometimes people make replace directives in their mod files that points to paths that aren't there.  (I'm looking at you, lotus.  There's a submodule.  Very creative.)  This makes go mod error.  That error is currently not raised very readably.
// - Go mod graph seems to... not actually be applying MVS before telling us its story.  I can't decide if that's troublesome or fine.
//    It's probably relatively unavoidable; it's clearer to state these facts than try to draw a picture of which selections were made by MVS when there's multiple projects as start points.
//    But it does mean you can end up seeing multiple versions of things even if you you have a single start module, which may be unintuitive.  Docs needed, at least.

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

func (sg Subgraph) ContentsOrdered() []ModuleAndVersion {
	r := make([]ModuleAndVersion, 0, len(sg.Contains))
	for mv := range sg.Contains {
		r = append(r, mv)
	}
	sort.SliceStable(r, func(i, j int) bool {
		return r[i].Version < r[j].Version
	})
	return r
}

func ParseModuleAndVersion(s string) ModuleAndVersion {
	// TODO: think about handling major versions more explicitly.
	// What the go tool is doing leaves us grouping v0 and v1 together right now, but higher versions are separate.
	// I don't think having a separate subgraph for each version >= 2 will produce a useful visualization,
	// so we should probably parse out and strip the major version from the end of the module name here.
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

// thoughts on node and edge color rules that could improve legibility:
// dominant rule first:
// - if the node is one of our starters, it's a deep red.
// - if the downstrea of an edge is one of those starters, it's a dark red-purple roll.
// - if the upstream of an edge is the focus, it's green-blue-gold roll.
// - we could create a preference for greenness or brightness in the newest versions of each module.
// all colors should be a starting point, and then each node gets a random spin of hue and saturation offset,
//  so that each node and the arrows originating from it are easier to track across a large and criss-crossed graph.

// build a ProcessedGraph, containing only those modules that are eventually depending on the focus.
func walkies(focus ModuleName, relationships []Relationship) ProcessedGraph {
	// REVIEW if this walk is actually the way we want to do this.
	// Currently there's a DFS here, and because of how it restarts at module granularity vs mod+ver granularity, it can and will draw versions of a module that don't actually link to the focus.
	// It's not super clear if that's useful or not.  I think it might be: it lets you see what you're likely to run into when upgrading things.
	// Also, DFS was easier to write, but BFS might be more useful: we might want our own 'rank' info (before graphviz) if we want to use it for color cues.

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
	// FIXME: this doesn't actually, erm, work.  Graphviz seems to be ignoring all my attempts to talk to it about explicit node rank/order within the subgraph for each module.  Haven't found correct incantation.

	// Future: considered discoloring things that have a bunch of dashes in the version name.  Those are non-tags.

	for moduleName, subgraph := range pg.Subgraphs {
		fmt.Fprintf(w, `subgraph "cluster_%s" {`+"\n", moduleName)
		fmt.Fprintf(w, `label="%s";`+"\n", moduleName)
		fmt.Fprintf(w, `rankdir=TB;`+"\n")
		for rank, node := range subgraph.ContentsOrdered() {
			fmt.Fprintf(w, `"%s" [label="%s" rank=%d];`+"\n", node, node.Version, rank)
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
