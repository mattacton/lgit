import React, {useEffect, useMemo, useState} from 'react';
import {Diff, Hunk, Decoration, parseDiff, getChangeKey, tokenize, markEdits} from 'react-diff-view';
import {api} from './api.js';
import {CommentThread, CommentForm} from './CommentThread.jsx';
import {fileAnchor} from './FileTree.jsx';
import {langForPath, shikiRefractor} from './shiki.js';

// Renders custom shiki tokens (with inline colors) and falls back to the
// default renderer for everything else (text, edit marks, ...).
function renderToken(token, defaultRender, i) {
    if (token.type === 'shiki') {
        return (
            <span key={i} style={token.color ? {color: token.color} : undefined}>
                {token.value !== undefined
                    ? token.value
                    : token.children && token.children.map((child, j) => renderToken(child, defaultRender, j))}
            </span>
        );
    }
    return defaultRender(token, i);
}

// Finds the change in hunks matching a (side, line) anchor.
function findChange(hunks, side, line) {
    for (const hunk of hunks) {
        for (const change of hunk.changes) {
            if (change.type === 'insert' && side === 'new' && change.lineNumber === line) return change;
            if (change.type === 'delete' && side === 'old' && change.lineNumber === line) return change;
            if (change.type === 'normal') {
                if (side === 'old' && change.oldLineNumber === line) return change;
                if (side === 'new' && change.newLineNumber === line) return change;
            }
        }
    }
    return null;
}

function lineOfChange(change, side) {
    if (change.type === 'normal') return side === 'old' ? change.oldLineNumber : change.newLineNumber;
    return change.lineNumber;
}

export default function FileDiffCard({
    prNumber, file, viewMode, threads, isViewed, onToggleViewed, onThreadsChanged,
}) {
    const path = file.new_path || file.old_path;
    const [collapsed, setCollapsed] = useState(isViewed);
    const [pending, setPending] = useState(null); // {changeKey, side, line}
    const [tokens, setTokens] = useState(null);

    const parsed = useMemo(() => {
        if (file.binary || !file.patch) return null;
        try {
            return parseDiff(file.patch)[0];
        } catch {
            return null;
        }
    }, [file]);

    const hunks = parsed ? parsed.hunks : [];
    const diffType = parsed ? parsed.type : 'modify';

    // Viewed checkbox collapses the file, like GitHub.
    useEffect(() => {
        setCollapsed(isViewed);
    }, [isViewed]);

    // Tokenize with Shiki (async); fall back to plain text + word-diff marks.
    useEffect(() => {
        let cancelled = false;
        setTokens(null);
        if (!hunks.length) return undefined;
        (async () => {
            const lang = langForPath(path);
            const refractor = await shikiRefractor(lang);
            try {
                const result = tokenize(hunks, {
                    highlight: !!refractor,
                    refractor: refractor || undefined,
                    language: lang || 'text',
                    enhancers: [markEdits(hunks, {type: 'block'})],
                });
                if (!cancelled) setTokens(result);
            } catch {
                // tokenization is best-effort; plain text rendering still works
            }
        })();
        return () => {
            cancelled = true;
        };
    }, [hunks, path]);

    // Inline comment widgets: existing threads + the in-progress form.
    const widgets = useMemo(() => {
        const map = {};
        const add = (changeKey, node) => {
            map[changeKey] = map[changeKey] ? [...map[changeKey], node] : [node];
        };
        for (const t of threads) {
            const change = findChange(hunks, t.side, t.line);
            if (!change) continue;
            add(getChangeKey(change), (
                <CommentThread key={`t${t.id}`} thread={t} onChanged={onThreadsChanged} />
            ));
        }
        if (pending) {
            add(pending.changeKey, (
                <CommentForm
                    key="new"
                    placeholder={`Comment on ${path}:${pending.line}`}
                    onCancel={() => setPending(null)}
                    onSubmit={async body => {
                        await api.createThread(prNumber, {
                            file_path: path,
                            line: pending.line,
                            side: pending.side,
                            body,
                        });
                        setPending(null);
                        await onThreadsChanged();
                    }}
                />
            ));
        }
        return Object.fromEntries(
            Object.entries(map).map(([k, nodes]) => [k, <div className="widget-stack">{nodes}</div>])
        );
    }, [threads, hunks, pending, path, prNumber, onThreadsChanged]);

    const gutterEvents = useMemo(() => ({
        onClick({side, change}) {
            if (!change) return;
            setPending({
                changeKey: getChangeKey(change),
                side,
                line: lineOfChange(change, side),
            });
        },
    }), []);

    const unresolved = threads.filter(t => !t.resolved).length;

    return (
        <section className="file-card" id={fileAnchor(path)}>
            <header className="file-card-header">
                <button className="collapse-toggle" onClick={() => setCollapsed(c => !c)}>
                    {collapsed ? '▸' : '▾'}
                </button>
                <span className={`file-path status-${file.status}`}>
                    {file.status === 'renamed' ? `${file.old_path} → ${file.new_path}` : path}
                </span>
                <span className={`file-status file-status-${file.status}`}>{file.status}</span>
                <span className="file-stats">
                    <span className="add">+{file.additions}</span> <span className="del">−{file.deletions}</span>
                    {unresolved > 0 && <span className="tree-comments"> {unresolved}💬</span>}
                </span>
                <label className="viewed-toggle">
                    <input type="checkbox" checked={isViewed} onChange={onToggleViewed} /> Viewed
                </label>
            </header>
            {!collapsed && (
                file.binary ? (
                    <div className="file-placeholder">Binary file not shown.</div>
                ) : !parsed ? (
                    <div className="file-placeholder">No content changes.</div>
                ) : (
                    <Diff
                        viewType={viewMode}
                        diffType={diffType}
                        hunks={hunks}
                        tokens={tokens || undefined}
                        renderToken={renderToken}
                        widgets={widgets}
                        gutterEvents={gutterEvents}
                    >
                        {hunksToRender => hunksToRender.flatMap((hunk, i) => {
                            const previous = hunksToRender[i - 1];
                            const skipped = previous
                                ? hunk.oldStart - (previous.oldStart + previous.oldLines)
                                : hunk.oldStart - 1;
                            const elements = [];
                            if (skipped > 0) {
                                elements.push(
                                    <Decoration key={`d${hunk.content}`}>
                                        <span className="hunk-sep">⋯ {skipped} unchanged lines</span>
                                    </Decoration>
                                );
                            }
                            elements.push(<Hunk key={hunk.content} hunk={hunk} />);
                            return elements;
                        })}
                    </Diff>
                )
            )}
        </section>
    );
}
