// DiffDuck tree-sitter shim, compiled together with the tree-sitter core and ONE grammar into
// a wasm32-wasi reactor module (see scripts/build-treesitter-wasm.sh).
//
// It is language agnostic: the build passes -DTS_LANGUAGE=tree_sitter_<lang>, so adding a
// language never edits this file.
//
// dd_parse flattens the whole syntax tree into fixed-size records in pre-order, so the Go host
// crosses the WASM boundary a constant number of times per file instead of once per node.
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include "tree_sitter/api.h"

#ifndef TS_LANGUAGE
#error "build with -DTS_LANGUAGE=tree_sitter_<lang>"
#endif

const TSLanguage *TS_LANGUAGE(void);

// Provided by the Go host: nonzero when the caller has given up, such as on cancellation or a
// timeout. tree-sitter asks every 100 parser operations, so a parse stops promptly.
__attribute__((import_module("dd"), import_name("should_stop"))) extern uint32_t dd_should_stop(void);

static bool progress(TSParseState *state) {
  (void)state;
  return dd_should_stop() != 0;
}

typedef struct {
  const char *src;
  uint32_t len;
} input;

static const char *read_input(void *payload, uint32_t byte, TSPoint point, uint32_t *read) {
  (void)point;
  input *in = payload;
  if (byte >= in->len) {
    *read = 0;
    return "";
  }
  *read = in->len - byte;
  return in->src + byte;
}

// One syntax node. The Go host decodes this layout (internal/adapters/treesitter/tree.go).
typedef struct {
  uint32_t symbol;
  uint32_t parent; // index of the parent record; UINT32_MAX for the root
  uint32_t start_byte, end_byte;
  uint32_t start_row, start_col, end_row, end_col;
  uint16_t field;  // field id within the parent, 0 when none
  uint8_t named;
  uint8_t flags;   // DD_ERROR | DD_MISSING | DD_HAS_ERROR | DD_EXTRA
} dd_node;

_Static_assert(sizeof(dd_node) == 36, "dd_node layout is part of the host ABI");

enum { DD_ERROR = 1, DD_MISSING = 2, DD_HAS_ERROR = 4, DD_EXTRA = 8 };

// Status of the last dd_parse call.
enum {
  DD_OK = 0,
  DD_ERR_LANGUAGE = 1,  // the grammar's ABI is not supported by this core
  DD_ERR_PARSE = 2,     // the parser returned no tree
  DD_ERR_MEMORY = 3,    // an allocation failed
  DD_ERR_DEPTH = 4,     // the tree is nested deeper than DD_MAX_DEPTH
  DD_ERR_STOPPED = 5,   // the host asked the parse to stop
};

#define DD_MAX_DEPTH 2048

static dd_node *out = NULL;
static uint32_t out_cap = 0;
static uint32_t status = DD_OK;
static uint32_t stack[DD_MAX_DEPTH];

static int reserve(uint32_t n) {
  if (n < out_cap) return 1;
  uint32_t next = out_cap ? out_cap * 2 : 4096;
  if (next <= out_cap) return 0;
  dd_node *p = realloc(out, (size_t)next * sizeof(dd_node));
  if (!p) return 0;
  out = p;
  out_cap = next;
  return 1;
}

// Parses len bytes at src. Returns the number of records at dd_result(), or 0 with
// dd_status() explaining why.
uint32_t dd_parse(const char *src, uint32_t len) {
  status = DD_OK;
  TSParser *parser = ts_parser_new();
  if (!parser) { status = DD_ERR_MEMORY; return 0; }
  if (!ts_parser_set_language(parser, TS_LANGUAGE())) {
    ts_parser_delete(parser);
    status = DD_ERR_LANGUAGE;
    return 0;
  }
  input in = {src, len};
  TSInput ts_input = {&in, read_input, TSInputEncodingUTF8, NULL};
  TSParseOptions options = {NULL, progress};
  TSTree *tree = ts_parser_parse_with_options(parser, NULL, ts_input, options);
  ts_parser_delete(parser);
  if (!tree) {
    status = dd_should_stop() ? DD_ERR_STOPPED : DD_ERR_PARSE;
    return 0;
  }

  TSTreeCursor cursor = ts_tree_cursor_new(ts_tree_root_node(tree));
  uint32_t n = 0;
  uint32_t depth = 0;
  uint32_t parent = UINT32_MAX;
  for (;;) {
    TSNode node = ts_tree_cursor_current_node(&cursor);
    if (!reserve(n)) { status = DD_ERR_MEMORY; n = 0; break; }
    TSPoint s = ts_node_start_point(node), e = ts_node_end_point(node);
    dd_node *r = &out[n];
    r->symbol = ts_node_symbol(node);
    r->parent = parent;
    r->start_byte = ts_node_start_byte(node);
    r->end_byte = ts_node_end_byte(node);
    r->start_row = s.row;
    r->start_col = s.column;
    r->end_row = e.row;
    r->end_col = e.column;
    r->field = ts_tree_cursor_current_field_id(&cursor);
    r->named = ts_node_is_named(node);
    r->flags = (ts_node_is_error(node) ? DD_ERROR : 0) | (ts_node_is_missing(node) ? DD_MISSING : 0) |
               (ts_node_has_error(node) ? DD_HAS_ERROR : 0) | (ts_node_is_extra(node) ? DD_EXTRA : 0);
    uint32_t me = n++;
    if (ts_tree_cursor_goto_first_child(&cursor)) {
      if (depth == DD_MAX_DEPTH) { status = DD_ERR_DEPTH; n = 0; break; }
      stack[depth++] = parent;
      parent = me;
      continue;
    }
    for (;;) {
      if (ts_tree_cursor_goto_next_sibling(&cursor)) break;
      if (!ts_tree_cursor_goto_parent(&cursor)) goto done;
      parent = stack[--depth];
    }
  }
done:
  ts_tree_cursor_delete(&cursor);
  ts_tree_delete(tree);
  return n;
}

dd_node *dd_result(void) { return out; }
uint32_t dd_status(void) { return status; }

uint32_t dd_symbol_count(void) { return ts_language_symbol_count(TS_LANGUAGE()); }
const char *dd_symbol_name(uint32_t symbol) { return ts_language_symbol_name(TS_LANGUAGE(), (TSSymbol)symbol); }
uint32_t dd_field_count(void) { return ts_language_field_count(TS_LANGUAGE()); }
const char *dd_field_name(uint32_t field) { return ts_language_field_name_for_id(TS_LANGUAGE(), (TSFieldId)field); }

void *dd_alloc(uint32_t n) { return malloc(n); }
void dd_free(void *p) { free(p); }
