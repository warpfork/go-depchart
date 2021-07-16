go-depchart
===========

This is a quick-and-dirty tool for drawing graphs of golang module relationships.

Its purpose is to visualize what kind of relationship downstream repos have to an upstream repo.
The intention is that this should be useful information for:

- planning changes in the upstream repo and seeing what their impact will be;
- to help coordinate propagating any changes down through the graph;
- and to see the overall "health" of the graph (how many different versions does it contain?).

The level of polish of this tool is currently not high.
Certain behaviors are hardcoded.
Some setup is required.


License
-------

SPDX-License-Identifier: Apache-2.0 OR MIT
