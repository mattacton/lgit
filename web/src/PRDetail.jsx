import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {api} from './api.js';
import {StateBadge} from './ui.jsx';
import FileTree, {fileAnchor} from './FileTree.jsx';
import FileDiffCard, {PathParts} from './FileDiffCard.jsx';
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

const LARGE_PR_BYTES = 400_000;
const LARGE_PR_FILES = 20;

function BaseTarget({pr, busy, onChanged}) {
    const [editing, setEditing] = useState(false);
    const [branches, setBranches] = useState(null);
    const [base, setBase] = useState(pr.base_ref);
    const [saving, setSaving] = useState(false);
    const [err, setErr] = useState(null);
    const live = pr.state !== 'merged' && pr.state !== 'closed';

    const startEdit = async () => {
        setEditing(true);
        setBase(pr.base_ref);
        setErr(null);
        try {
            setBranches(await api.listBranches());
        } catch (e) {
            setErr(e.message);
        }
    };

    const save = async () => {
        const next = base.trim();
        if (next === pr.base_ref) {
            setEditing(false);
            return;
        }
        if (pr.state === 'approved' && !window.confirm(
            `Change the target from ${pr.base_ref} to ${next}? This clears the approval.`,
        )) return;
        setSaving(true);
        setErr(null);
        try {
            const res = await api.setBase(pr.number, next);
            setEditing(false);
            await onChanged(res);
        } catch (e) {
            setErr(e.message);
        } finally {
            setSaving(false);
        }
    };

    if (!live) return <span className="meta-branch">{pr.base_ref}</span>;
    if (!editing) {
        return (
            <span className="base-target">
                <span className="meta-branch">{pr.base_ref}</span>
                <button
                    className="btn btn-small"
                    disabled={busy}
                    title="Change the branch this PR targets"
                    onClick={startEdit}
                >
                    change
                </button>
            </span>
        );
    }
    const options = [...new Set([pr.base_ref, ...(branches || [])])].filter(b => b && b !== pr.head_ref);
    return (
        <span className="base-target">
            <select
                aria-label="Target branch"
                value={options.includes(base) ? base : pr.base_ref}
                disabled={!branches || saving}
                onChange={e => setBase(e.target.value)}
            >
                {options.map(b => <option key={b} value={b}>{b}</option>)}
            </select>
            <button className="btn btn-small" disabled={saving || !branches} onClick={save}>set</button>
            <button className="btn btn-small" disabled={saving} onClick={() => setEditing(false)}>cancel</button>
            {err && <span className="base-target-error">{err}</span>}
        </span>
    );
}

export default function PRDetail({number}) {
    const [data, setData] = useState(null); // {pr, files}
    const [threads, setThreads] = useState([]);
    const [error, setError] = useState(null);
    const [viewMode, setViewMode] = useState('split');
    const [diffScope, setDiffScope] = useState('full');
    const [filter, setFilter] = useState('');
    const [viewed, setViewed] = useState(() => loadViewed(number));
    const [reviewBody, setReviewBody] = useState('');
    const [showPRComment, setShowPRComment] = useState(false);
    const [busy, setBusy] = useState(false);
    const [mergeCleanup, setMergeCleanup] = useState(true);
    const [notice, setNotice] = useState(null);

    const reload = useCallback(async () => {
        try {
            const since = diffScope === 'last-review' ? 'last-review' : '';
            const [diff, ths] = await Promise.all([api.getDiff(number, since), api.listThreads(number)]);
            setData(diff);
            setThreads(ths);
            setError(null);
        } catch (e) {
            setError(e.message);
        }
    }, [number, diffScope]);

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

    const doMerge = async () => {
        const {pr} = data;
        if (!window.confirm(`Rebase & merge PR #${number} into ${pr.base_ref}?`)) return;
        setBusy(true);
        try {
            const res = await api.mergePR(number, mergeCleanup);
            setData(d => (d ? {...d, pr: res.pr} : d));
            setNotice(res.cleanup_message || null);
            setError(null);
        } catch (e) {
            setError(e.message);
        } finally {
            setBusy(false);
        }
    };

    const handleBaseChanged = async res => {
        setData(d => (d ? {...d, pr: res.pr} : d));
        setNotice(res.approval_cleared
            ? `Target is now ${res.pr.base_ref}. Approval was cleared — re-review the new diff.`
            : `Target is now ${res.pr.base_ref}.`);
        setError(null);
        await reload();
    };

    const addPRComment = async body => {
        const thread = await api.createThread(number, {file_path: '', line: 0, side: 'new', body});
        setThreads(prev => [...prev, thread]);
        setShowPRComment(false);
        await reloadThreads();
    };

    if (error && !data) return <div className="banner banner-error">{error}</div>;
    if (!data) return <div className="loading">Loading…</div>;

    const {pr, files, last_review: lastReview, from_sha: fromSHA} = data;
    const prThreads = threadsByFile.get('') || [];
    const shownFiles = filter
        ? files.filter(f => (f.new_path || f.old_path).toLowerCase().includes(filter.toLowerCase()))
        : files;
    const unresolvedCount = threads.filter(t => !t.resolved).length;
    const resolvedCount = threads.length - unresolvedCount;
    // Files that have threads but are no longer in the diff (the commented
    // code was reverted or the diff scope excludes them) still get a card, so
    // their conversations never vanish from the review.
    const diffPaths = new Set(files.map(f => f.new_path || f.old_path));
    const orphanPaths = [...threadsByFile.keys()]
        .filter(p => p && !diffPaths.has(p))
        .filter(p => !filter || p.toLowerCase().includes(filter.toLowerCase()))
        .sort();
    const totalPatchBytes = files.reduce((sum, f) => sum + (f.patch ? f.patch.length : 0), 0);
    const defaultCollapsed = files.length > LARGE_PR_FILES || totalPatchBytes > LARGE_PR_BYTES;
    const needsReviewStateReminder = pr.state === 'open' && unresolvedCount > 0;
    const totalAdd = files.reduce((sum, f) => sum + (f.additions || 0), 0);
    const totalDel = files.reduce((sum, f) => sum + (f.deletions || 0), 0);
    const fromShort = (fromSHA || pr.merge_base_sha).slice(0, 8);
    const headShort = pr.head_sha.slice(0, 8);

    return (
        <main className="pr-detail">
            {error && <div className="banner banner-error">{error}</div>}
            {notice && (
                <div className="banner banner-info">
                    <span className="banner-icon">i</span>
                    {notice}
                </div>
            )}
            <div className="pr-header">
                <div className="pr-header-title">
                    <h1>
                        {pr.title} <span className="pr-number">#{pr.number}</span>
                    </h1>
                    <StateBadge state={pr.state} />
                </div>
                <div className="pr-header-meta">
                    <span className="meta-branches">
                        <span className="meta-branch">{pr.head_ref}</span>
                        <span>→</span>
                        <BaseTarget pr={pr} busy={busy} onChanged={handleBaseChanged} />
                    </span>
                    <span className="sep">·</span>
                    <span title={`${fromSHA || pr.merge_base_sha}..${pr.head_sha}`}>{fromShort}..{headShort}</span>
                    <span className="sep">·</span>
                    <span className={unresolvedCount > 0 ? 'meta-unresolved' : undefined}>
                        {unresolvedCount} unresolved
                    </span>
                    {resolvedCount > 0 && (
                        <>
                            <span className="sep">·</span>
                            <span>{resolvedCount} resolved</span>
                        </>
                    )}
                    <button className="btn btn-small" onClick={reload}>↻ Refresh</button>
                </div>
                {pr.body && <p className="pr-body">{pr.body}</p>}
            </div>

            <div className="review-bar">
                <input
                    className="review-summary"
                    placeholder="Leave a review summary…"
                    value={reviewBody}
                    onChange={e => setReviewBody(e.target.value)}
                />
                <button className="btn btn-approve" disabled={busy} onClick={() => submitReview('approve')}>
                    <span aria-hidden="true">✓</span><span>Approve</span>
                </button>
                <button className="btn btn-request" disabled={busy} onClick={() => submitReview('request_changes')}>
                    Request changes
                </button>
                {pr.state !== 'merged' && pr.state !== 'closed' && (
                    <span className="merge-controls">
                        <button className="btn btn-merge" disabled={busy} onClick={doMerge}>
                            Rebase &amp; merge
                        </button>
                        <label
                            className="cleanup-toggle"
                            title="After merging, remove the PR's worktree and delete its branch"
                        >
                            <input
                                type="checkbox"
                                checked={mergeCleanup}
                                onChange={e => setMergeCleanup(e.target.checked)}
                            />
                            clean up worktree
                        </label>
                    </span>
                )}
            </div>

            <div className="view-toggles">
                <div className="seg">
                    <button
                        className={diffScope === 'full' ? 'seg-btn seg-btn-active' : 'seg-btn'}
                        onClick={() => setDiffScope('full')}
                    >
                        Full diff
                    </button>
                    <button
                        className={diffScope === 'last-review' ? 'seg-btn seg-btn-active' : 'seg-btn'}
                        onClick={() => setDiffScope('last-review')}
                    >
                        Since last review
                    </button>
                </div>
                <div className="seg">
                    <button
                        className={viewMode === 'split' ? 'seg-btn seg-btn-active' : 'seg-btn'}
                        onClick={() => setViewMode('split')}
                    >
                        Side-by-side
                    </button>
                    <button
                        className={viewMode === 'unified' ? 'seg-btn seg-btn-active' : 'seg-btn'}
                        onClick={() => setViewMode('unified')}
                    >
                        Unified
                    </button>
                </div>
                <span className="diff-summary">
                    {files.length} files · <span className="add">+{totalAdd}</span> <span className="del">−{totalDel}</span>
                </span>
            </div>

            {needsReviewStateReminder && (
                <div className="banner banner-warn">
                    You have unresolved feedback on an open PR. Click <strong>Request changes</strong> when you want
                    the agent to act on it; that records a review checkpoint for “Since last review”.
                </div>
            )}

            <div className="pr-layout">
                <aside className="sidebar">
                    <div className="file-filter-wrap">
                        <input
                            className="file-filter"
                            placeholder="Filter files…"
                            value={filter}
                            onChange={e => setFilter(e.target.value)}
                        />
                    </div>
                    <FileTree
                        files={shownFiles}
                        viewed={viewed}
                        threadsByFile={threadsByFile}
                    />
                </aside>

                <div className="diff-column">
                    {defaultCollapsed && (
                        <div className="banner banner-info">
                            <span className="banner-icon">i</span>
                            Large diff detected — files start collapsed and render only when opened.
                        </div>
                    )}
                    {diffScope === 'last-review' && (
                        <div className="banner banner-info">
                            <span className="banner-icon">i</span>
                            {lastReview && lastReview.head_sha
                                ? <span>Showing changes after review #{lastReview.id} at <code>{lastReview.head_sha.slice(0, 8)}</code>.</span>
                                : 'No reviewed head SHA is available yet, so this matches the full diff.'}
                        </div>
                    )}
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
                                defaultCollapsed={defaultCollapsed}
                                onToggleViewed={() => toggleViewed(path)}
                                onThreadsChanged={reloadThreads}
                            />
                        );
                    })}
                    {orphanPaths.map(p => (
                        <section key={p} className="file-card" id={fileAnchor(p)}>
                            <header className="file-card-header file-card-header-plain">
                                <span className="file-path"><PathParts path={p} /></span>
                                <span className="badge badge-sm badge-outdated">not in this diff</span>
                            </header>
                            <div className="file-threads">
                                <div className="widget-stack">
                                    {(threadsByFile.get(p) || []).map(t => (
                                        <CommentThread key={t.id} thread={t} onChanged={reloadThreads} inline />
                                    ))}
                                </div>
                            </div>
                        </section>
                    ))}
                    {shownFiles.length === 0 && orphanPaths.length === 0 && (
                        <p className="empty">
                            {diffScope === 'last-review' ? 'No changes since the last review.' : 'No files match.'}
                        </p>
                    )}

                    <section className="conversation">
                        <h2>Conversation</h2>
                        {prThreads.length === 0 && !showPRComment && (
                            <p className="empty">No PR-level comments.</p>
                        )}
                        <div className="conversation-threads">
                            {prThreads.map(t => (
                                <CommentThread key={t.id} thread={t} onChanged={reloadThreads} />
                            ))}
                        </div>
                        {showPRComment ? (
                            <CommentForm
                                onSubmit={addPRComment}
                                onCancel={() => setShowPRComment(false)}
                                placeholder="Leave a PR-level comment…"
                            />
                        ) : (
                            <button className="add-comment-btn" onClick={() => setShowPRComment(true)}>
                                + Add PR comment…
                            </button>
                        )}
                    </section>
                </div>
            </div>
        </main>
    );
}
