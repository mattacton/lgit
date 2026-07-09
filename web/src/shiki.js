// Shiki -> react-diff-view integration.
//
// react-diff-view's `tokenize` accepts a refractor-like object whose
// `highlight(text, language)` returns a hast-ish node list. We satisfy that
// contract with Shiki: each themed token becomes a `{type: 'shiki', color}`
// node whose color is rendered by the custom renderToken in FileDiffCard.
//
// We use shiki/core with the JavaScript regex engine and a curated language
// set so the built (and Go-embedded) assets stay small.

import {createHighlighterCore} from 'shiki/core';
import {createJavaScriptRegexEngine} from 'shiki/engine/javascript';

// A minimal theme whose only job is to tag three token classes with CSS
// custom-property colors. Shiki's normalizeTheme records any non-`#`
// foreground as a color-replacement and hands `token.color` back verbatim, so
// `var(--k)` etc. survive to render time and follow the active theme with no
// re-tokenize. Everything else falls back to the (non-var) default foreground,
// which the highlight() adapter drops so it inherits the diff line color.
const lgitTheme = {
    name: 'lgit',
    type: 'dark',
    settings: [
        // Global default; dropped at render (not a var color) so text inherits
        // the diff line color (context / add / delete).
        {settings: {foreground: '#aaaaaa', background: '#000000'}},
        {
            scope: ['keyword', 'storage', 'storage.type', 'storage.modifier', 'keyword.control'],
            settings: {foreground: 'var(--k)'},
        },
        // Operators (=, =>, &&, !, >, …) read as plain code, not keywords.
        {scope: ['keyword.operator'], settings: {foreground: '#aaaaaa'}},
        {
            scope: ['string', 'punctuation.definition.string', 'string.regexp'],
            settings: {foreground: 'var(--s)'},
        },
        {
            scope: ['entity.name.function', 'support.function', 'variable.function'],
            settings: {foreground: 'var(--fn)'},
        },
    ],
};

// Each entry lazy-loads its grammar; Vite splits these into per-lang chunks
// fetched on demand.
const LANG_LOADERS = {
    go: () => import('shiki/langs/go.mjs'),
    javascript: () => import('shiki/langs/javascript.mjs'),
    jsx: () => import('shiki/langs/jsx.mjs'),
    typescript: () => import('shiki/langs/typescript.mjs'),
    tsx: () => import('shiki/langs/tsx.mjs'),
    python: () => import('shiki/langs/python.mjs'),
    ruby: () => import('shiki/langs/ruby.mjs'),
    rust: () => import('shiki/langs/rust.mjs'),
    java: () => import('shiki/langs/java.mjs'),
    c: () => import('shiki/langs/c.mjs'),
    css: () => import('shiki/langs/css.mjs'),
    html: () => import('shiki/langs/html.mjs'),
    json: () => import('shiki/langs/json.mjs'),
    yaml: () => import('shiki/langs/yaml.mjs'),
    toml: () => import('shiki/langs/toml.mjs'),
    markdown: () => import('shiki/langs/markdown.mjs'),
    shellscript: () => import('shiki/langs/shellscript.mjs'),
    sql: () => import('shiki/langs/sql.mjs'),
    dockerfile: () => import('shiki/langs/dockerfile.mjs'),
    makefile: () => import('shiki/langs/makefile.mjs'),
};

const EXT_TO_LANG = {
    go: 'go', js: 'javascript', mjs: 'javascript', cjs: 'javascript',
    jsx: 'jsx', ts: 'typescript', tsx: 'tsx', py: 'python', rb: 'ruby',
    rs: 'rust', java: 'java', c: 'c', h: 'c',
    css: 'css', html: 'html', htm: 'html',
    json: 'json', yaml: 'yaml', yml: 'yaml', toml: 'toml',
    md: 'markdown', sh: 'shellscript', bash: 'shellscript', zsh: 'shellscript',
    sql: 'sql',
};

export function langForPath(path) {
    if (!path) return null;
    const base = path.split('/').pop().toLowerCase();
    if (base === 'dockerfile') return 'dockerfile';
    if (base === 'makefile' || base === 'gnumakefile') return 'makefile';
    const ext = base.includes('.') ? base.split('.').pop() : '';
    return EXT_TO_LANG[ext] || null;
}

let highlighterPromise = null;

function getHighlighter() {
    if (!highlighterPromise) {
        highlighterPromise = createHighlighterCore({
            themes: [lgitTheme],
            langs: [],
            engine: createJavaScriptRegexEngine({forgiving: true}),
        });
    }
    return highlighterPromise;
}

// Returns a refractor-compatible `{highlight}` object for the language, or
// null if the language is unknown/unloadable.
export async function shikiRefractor(lang) {
    if (!lang || !LANG_LOADERS[lang]) return null;
    let highlighter;
    try {
        highlighter = await getHighlighter();
        if (!highlighter.getLoadedLanguages().includes(lang)) {
            await highlighter.loadLanguage(await LANG_LOADERS[lang]());
        }
    } catch {
        return null;
    }
    return {
        highlight(text, language) {
            const lines = highlighter.codeToTokensBase(text, {
                lang: language,
                theme: 'lgit',
            });
            const nodes = [];
            lines.forEach((tokens, i) => {
                if (i > 0) nodes.push({type: 'text', value: '\n'});
                for (const token of tokens) {
                    // Only forward our CSS-var colors; anything else (the theme
                    // default) is dropped so the token inherits the line color.
                    const color = token.color && token.color.startsWith('var(') ? token.color : undefined;
                    nodes.push({
                        type: 'shiki',
                        color,
                        children: [{type: 'text', value: token.content}],
                    });
                }
            });
            return nodes;
        },
    };
}
