/**
 * A small, safe Markdown reader for model output.
 *
 * The dashboard had no Markdown dependency. Adding a full CommonMark renderer
 * (and its sanitiser) for one panel would be a lot of new surface for the job,
 * so this reads the subset a model actually emits — headings, fenced code,
 * lists, quotes, rules, emphasis, inline code and links — into a typed tree.
 *
 * Two properties matter and are why this is a parser rather than a regex that
 * emits HTML:
 *
 *   - nothing is ever interpreted as HTML. Text is carried as data and React
 *     escapes it on render, so a model that answers with `<script>` cannot
 *     execute it.
 *   - only `http`, `https`, `mailto` and site-relative links survive. A
 *     `javascript:` or `data:` href is dropped to its label rather than
 *     rendered, because model output is untrusted input.
 *
 * It is deliberately not a complete Markdown implementation: there are no
 * tables, footnotes, reference links or nested lists. Anything unrecognised
 * degrades to text rather than disappearing.
 */

export type MdInline =
  | { type: 'text'; value: string }
  | { type: 'code'; value: string }
  | { type: 'strong'; children: MdInline[] }
  | { type: 'em'; children: MdInline[] }
  | { type: 'link'; href: string; children: MdInline[] };

export type MdBlock =
  | { type: 'paragraph'; children: MdInline[] }
  | { type: 'heading'; level: number; children: MdInline[] }
  | { type: 'code'; language: string; value: string }
  | { type: 'list'; ordered: boolean; items: MdInline[][] }
  | { type: 'quote'; children: MdInline[] }
  | { type: 'rule' };

/** Maximum emphasis nesting, so pathological output cannot blow the stack. */
const MAX_INLINE_DEPTH = 4;

/**
 * Approve a link target, or return null to render it as plain text.
 *
 * Relative targets are allowed because a model may legitimately reference a
 * dashboard route; scheme-qualified targets must be on an allowlist.
 */
export function safeHref(href: string): string | null {
  const trimmed = href.trim();
  if (trimmed.length === 0) return null;
  if (trimmed.startsWith('/') || trimmed.startsWith('#')) return trimmed;
  const scheme = /^([a-zA-Z][a-zA-Z0-9+.-]*):/.exec(trimmed);
  if (!scheme) return null;
  const allowed = new Set(['http', 'https', 'mailto']);
  return allowed.has((scheme[1] ?? '').toLowerCase()) ? trimmed : null;
}

function isWordChar(value: string | undefined): boolean {
  return value != null && /[A-Za-z0-9]/.test(value);
}

/**
 * Parse a single line (or a joined paragraph) into inline nodes.
 *
 * Scanning left to right with an explicit precedence — code, then links, then
 * strong, then emphasis — keeps `**a**` from being read as two emphasis spans,
 * and keeps emphasis from being applied inside a code span.
 */
export function parseInline(text: string, depth = 0): MdInline[] {
  const out: MdInline[] = [];
  let i = 0;

  while (i < text.length) {
    const rest = text.slice(i);

    const code = /^`([^`\n]+)`/.exec(rest);
    if (code) {
      out.push({ type: 'code', value: code[1] ?? '' });
      i += code[0].length;
      continue;
    }

    // A destination may contain one level of balanced parentheses, which is
    // what makes `[x](javascript:alert(1))` recognisable as a link at all. If
    // it were not recognised, the unsafe destination would survive as literal
    // text instead of being sanitised away.
    const link = /^\[([^\]\n]*)\]\(((?:[^()\s]|\([^()\s]*\))*)\)/.exec(rest);
    if (link) {
      const label = link[1] ?? '';
      const href = safeHref(link[2] ?? '');
      out.push(
        href
          ? { type: 'link', href, children: parseInline(label, depth + 1) }
          : { type: 'text', value: label },
      );
      i += link[0].length;
      continue;
    }

    if (depth < MAX_INLINE_DEPTH) {
      const strong = /^(\*\*|__)([^\n]+?)\1/.exec(rest);
      if (strong) {
        out.push({ type: 'strong', children: parseInline(strong[2] ?? '', depth + 1) });
        i += strong[0].length;
        continue;
      }

      const starEm = /^\*([^*\n]+)\*/.exec(rest);
      if (starEm) {
        out.push({ type: 'em', children: parseInline(starEm[1] ?? '', depth + 1) });
        i += starEm[0].length;
        continue;
      }

      // `_` only opens emphasis on a word boundary: otherwise identifiers like
      // `max_output_tokens` would be split into emphasis on every render.
      const scoreEm = /^_([^_\n]+)_/.exec(rest);
      if (scoreEm && !isWordChar(text[i - 1]) && !isWordChar(text[i + scoreEm[0].length])) {
        out.push({ type: 'em', children: parseInline(scoreEm[1] ?? '', depth + 1) });
        i += scoreEm[0].length;
        continue;
      }
    }

    // Plain run: advance to the next character that could open an inline span.
    const next = rest.slice(1).search(/[`*_\[]/);
    const end = next === -1 ? rest.length : next + 1;
    out.push({ type: 'text', value: rest.slice(0, end) });
    i += end;
  }

  return coalesceText(out);
}

/**
 * Merge neighbouring text nodes.
 *
 * The scanner splits a plain run every time it passes a marker character that
 * turns out not to open a span (the underscores in `max_output_tokens`, say).
 * The pieces are adjacent text and should read as one run, both for the
 * renderer and for anything comparing the tree.
 */
function coalesceText(nodes: MdInline[]): MdInline[] {
  const out: MdInline[] = [];
  for (const node of nodes) {
    const previous = out[out.length - 1];
    if (node.type === 'text' && previous?.type === 'text') {
      out[out.length - 1] = { type: 'text', value: previous.value + node.value };
      continue;
    }
    out.push(node);
  }
  return out;
}

const FENCE = /^```\s*([\w+#.-]*)\s*$/;
const HEADING = /^(#{1,6})\s+(.*)$/;
const RULE = /^(-{3,}|\*{3,}|_{3,})$/;
const QUOTE = /^>\s?/;
const UNORDERED = /^[-*+]\s+/;
const ORDERED = /^\d+[.)]\s+/;
const UNORDERED_ITEM = /^[-*+]\s+(.*)$/;
const ORDERED_ITEM = /^\d+[.)]\s+(.*)$/;

/** True when a line begins a block, so a paragraph must stop before it. */
function startsBlock(line: string): boolean {
  const trimmed = line.trim();
  return (
    FENCE.test(trimmed) ||
    HEADING.test(trimmed) ||
    RULE.test(trimmed) ||
    QUOTE.test(line) ||
    UNORDERED.test(line) ||
    ORDERED.test(line)
  );
}

/** Parse a Markdown document into blocks. */
export function parseMarkdown(source: string): MdBlock[] {
  const lines = source.replace(/\r\n/g, '\n').split('\n');
  const blocks: MdBlock[] = [];
  // `noUncheckedIndexedAccess` makes every index a `string | undefined`; the
  // bounds are known to hold, so this reads them once instead of repeating
  // defensive checks in each branch.
  const at = (index: number): string => lines[index] ?? '';
  let i = 0;

  while (i < lines.length) {
    const line = at(i);

    if (!line.trim()) {
      i++;
      continue;
    }

    const fence = FENCE.exec(line.trim());
    if (fence) {
      const language = fence[1] ?? '';
      const body: string[] = [];
      i++;
      while (i < lines.length && !FENCE.test(at(i).trim())) {
        body.push(at(i));
        i++;
      }
      // An unterminated fence still yields its code: the model was cut off
      // mid-block, and dropping the partial output would hide that.
      if (i < lines.length) i++;
      blocks.push({ type: 'code', language, value: body.join('\n') });
      continue;
    }

    if (RULE.test(line.trim())) {
      blocks.push({ type: 'rule' });
      i++;
      continue;
    }

    const heading = HEADING.exec(line.trim());
    if (heading) {
      blocks.push({
        type: 'heading',
        level: (heading[1] ?? '').length,
        children: parseInline((heading[2] ?? '').trim()),
      });
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const quoted: string[] = [];
      while (i < lines.length && QUOTE.test(at(i))) {
        quoted.push(at(i).replace(QUOTE, ''));
        i++;
      }
      blocks.push({ type: 'quote', children: parseInline(quoted.join(' ')) });
      continue;
    }

    if (UNORDERED.test(line) || ORDERED.test(line)) {
      const ordered = ORDERED.test(line);
      const items: MdInline[][] = [];
      while (i < lines.length) {
        const item = (ordered ? ORDERED_ITEM : UNORDERED_ITEM).exec(at(i));
        if (!item) break;
        items.push(parseInline((item[1] ?? '').trim()));
        i++;
      }
      blocks.push({ type: 'list', ordered, items });
      continue;
    }

    const paragraph: string[] = [];
    while (i < lines.length && at(i).trim() && !startsBlock(at(i))) {
      paragraph.push(at(i).trim());
      i++;
    }
    if (paragraph.length === 0) {
      // Defensive: a line that starts a block was rejected above, so this can
      // only happen on an empty read. Step forward rather than loop forever.
      i++;
      continue;
    }
    blocks.push({ type: 'paragraph', children: parseInline(paragraph.join(' ')) });
  }

  return blocks;
}

/** Plain text of an inline tree, for copying or previews. */
export function inlineText(nodes: MdInline[]): string {
  return nodes
    .map((node) => {
      switch (node.type) {
        case 'text':
        case 'code':
          return node.value;
        default:
          return inlineText(node.children);
      }
    })
    .join('');
}
