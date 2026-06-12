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
import githubLight from 'shiki/themes/github-light.mjs';

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
            themes: [githubLight],
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
                theme: 'github-light',
            });
            const nodes = [];
            lines.forEach((tokens, i) => {
                if (i > 0) nodes.push({type: 'text', value: '\n'});
                for (const token of tokens) {
                    nodes.push({
                        type: 'shiki',
                        color: token.color,
                        children: [{type: 'text', value: token.content}],
                    });
                }
            });
            return nodes;
        },
    };
}
