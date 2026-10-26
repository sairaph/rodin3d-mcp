// Package hideconsole stands in for github.com/ebitengine/hideconsole, which
// Ebitengine imports for its side effect: on Windows its init frees the
// console of a process that created its own (one started from Explorer).
// rodin3d-mcp links Ebitengine for the viewer window, but the same binary is
// the CLI, the terminal app and the MCP server, which need their console; the
// viewer process gets no console window anyway (it is started with
// CREATE_NO_WINDOW). go.mod replaces the module with this empty package.
package hideconsole
