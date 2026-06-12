import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {api} from './api.js';
import {StateBadge} from './ui.jsx';
import FileTree from './FileTree.jsx';
import FileDiffCard from './FileDiffCard.jsx';
import {CommentThread, CommentForm} from './CommentThread.jsx';

function viewedKey(number) {
    return `lgit:viewed:${number}`;
}

function loadViewed(number) {
    try {
        return JSON.parse(localStorage.getItem(viewedKey(number))) || {};
    } catch {
        return {};
    }
}

export default function PRDetail({number}) {
    const [data, setData] = useState(null); // {pr, files}
    const [threads, setThreads] = useState([]);
    const [error, setError] = useState(null);
    const [viewMode, setViewMode] = useState('split');
    const [filter, setFilter] = useState('');
    const [viewed, setViewed] = useState(() => loadViewed(number));
    const [reviewBody, setReviewBody] = useState('');
    const [showPRComment, setShowPRComment] = useState(false);
    const [busy, setBusy] = useState(false);

    const reload = useCallback(async () => {
        try {
            const [diff, ths] = await Promise.all([api.getDiff(number), api.listThreads(number)]);
            setData(diff);
            setThreads(ths);
            setError(null);
        } catch (e) {
            setError(e.message);
        }
    }, [number]);

    useEffect(() => {
        setViewed(loadViewed(number));
        reload();
    }, [number, reload]);

    const reloadThreads = useCallback(async () => {
        try {
            setThreads(await api.listThreads(number));
        } catch (e) {
            setError(e.message);
        }
    }, [number]);

    const toggleViewed = useCallback(path => {
        setViewed(prev => {
            const next = {...prev, [path]: !prev[path]};
            localStorage.setItem(viewedKey(number), JSON.stringify(next));
            return next;
        });
    }, [number]);

    const threadsByFile = useMemo(() => {
        const map = new Map();
        for (const t of threads) {
            const key = t.file_path || '';
            if (!map.has(key)) map.set(key, []);
            map.get(key).push(t);
        }
        return map;
    }, [threads]);

    const submitReview = async verdict => {
        setBusy(true);
        try {
            const res = await api.createReview(number, {verdict, body: reviewBody});
            setReviewBody('');
            setData(d => (d ? {...d, pr: res.pr} : d));
        } catch (e) {
            setError(e.message);
        } finally {
            setBusy(false);
        }
    };

    const addPRComment = async body => {
        await api.createThread(number, {file_path: '', line: 0, side: 'new', body});
        setShowPRComment(false);
        await reloadThreads();
    };

    if (error && !data) return <div className="error-banner">{error}</div>;
    if (!data) return <div className="loading">Loading…</div>;

    const {pr, files} = data;
    const prThreads = threadsByFile.get('') || [];
    const shownFiles = filter
        ? files.filter(f => (f.new_path || f.old_path).toLowerCase().includes(filter.toLowerCase()))
        : files;
    const unresolvedCount = threads.filter(t => !t.resolved).length;

    return (
        <main className="pr-detail">
            {error && <div className="error-banner">{error}</div>}
            <div className="pr-header">
                <div className="pr-header-title">
                    <h1>
                        {pr.title} <span className="pr-number">#{pr.number}</span>
                    </h1>
                    <StateBadge state={pr.state} />
                </div>
                <div className="pr-header-meta">
                    <code>{pr.head_ref}</code> → <code>{pr.base_ref}</code>
                    <span className="sep">·</span>
                    <code title={pr.merge_base_sha}>{pr.merge_base_sha.slice(0, 8)}</code>..
                    <code title={pr.head_sha}>{pr.head_sha.slice(0, 8)}</code>
                    <span className="sep">·</span>
                    {unresolvedCount} unresolved
                    <button className="btn btn-small" onClick={reload}>Refresh</button>
                </div>
                {pr.body && <p className="pr-body">{pr.body}</p>}
            </div>

            <div className="review-bar">
                <input
                    className="review-summary"
                    placeholder="Review summary (optional)"
                    value={reviewBody}
                    onChange={e => setReviewBody(e.target.value)}
                />
                <button className="btn btn-approve" disabled={busy} onClick={() => submitReview('approve')}>
                    Approve
                </button>
                <button className="btn btn-request" disabled={busy} onClick={() => submitReview('request_changes')}>
                    Request changes
                </button>
                <div className="view-toggle">
                    <button
                        className={viewMode === 'split' ? 'chip chip-active' : 'chip'}
                        onClick={() => setViewMode('split')}
                    >
                        Side-by-side
                    </button>
                    <button
                        className={viewMode === 'unified' ? 'chip chip-active' : 'chip'}
                        onClick={() => setViewMode('unified')}
                    >
                        Unified
                    </button>
                </div>
            </div>

            <div className="pr-layout">
                <aside className="sidebar">
                    <input
                        className="file-filter"
                        placeholder="Filter files…"
                        value={filter}
                        onChange={e => setFilter(e.target.value)}
                    />
                    <FileTree
                        files={shownFiles}
                        viewed={viewed}
                        threadsByFile={threadsByFile}
                    />
                </aside>

                <div className="diff-column">
                    {shownFiles.map(file => {
                        const path = file.new_path || file.old_path;
                        return (
                            <FileDiffCard
                                key={path}
                                prNumber={number}
                                file={file}
                                viewMode={viewMode}
                                threads={threadsByFile.get(path) || []}
                                isViewed={!!viewed[path]}
                                onToggleViewed={() => toggleViewed(path)}
                                onThreadsChanged={reloadThreads}
                            />
                        );
                    })}
                    {shownFiles.length === 0 && <p className="empty">No files match.</p>}

                    <section className="conversation">
                        <h2>Conversation</h2>
                        {prThreads.length === 0 && !showPRComment && (
                            <p className="empty">No PR-level comments.</p>
                        )}
                        {prThreads.map(t => (
                            <CommentThread key={t.id} thread={t} onChanged={reloadThreads} />
                        ))}
                        {showPRComment ? (
                            <CommentForm
                                onSubmit={addPRComment}
                                onCancel={() => setShowPRComment(false)}
                                placeholder="Leave a PR-level comment…"
                            />
                        ) : (
                            <button className="btn" onClick={() => setShowPRComment(true)}>
                                Add PR comment
                            </button>
                        )}
                    </section>
                </div>
            </div>
        </main>
    );
}
