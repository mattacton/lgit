import React, {useState} from 'react';
import {api} from './api.js';
import {timeAgo} from './ui.jsx';

export function CommentForm({onSubmit, onCancel, placeholder}) {
    const [body, setBody] = useState('');
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState(null);

    const submit = async () => {
        if (!body.trim()) return;
        setBusy(true);
        setError(null);
        try {
            await onSubmit(body);
            setBody('');
        } catch (e) {
            setError(e.message);
        } finally {
            setBusy(false);
        }
    };

    return (
        <div className="comment-form">
            {error && <div className="banner banner-error">{error}</div>}
            <textarea
                autoFocus
                value={body}
                placeholder={placeholder || 'Leave a comment…'}
                onChange={e => setBody(e.target.value)}
                onKeyDown={e => {
                    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') submit();
                }}
            />
            <div className="comment-form-actions">
                <button className="btn btn-primary" disabled={busy || !body.trim()} onClick={submit}>
                    Comment
                </button>
                {onCancel && (
                    <button className="btn" disabled={busy} onClick={onCancel}>
                        Cancel
                    </button>
                )}
            </div>
        </div>
    );
}

// Status + outdated pills, shared by both variants.
function StatusPills({thread}) {
    return (
        <>
            {thread.resolved
                ? <span className="badge badge-sm badge-resolved">Resolved</span>
                : <span className="badge badge-sm badge-thread-open">Open</span>}
            {thread.outdated && <span className="badge badge-sm badge-outdated">Outdated</span>}
        </>
    );
}

// One thread renders in two shapes: a PR-level card (header bar + body) for
// conversation threads, and a bare comment card for inline diff threads. Only
// the presentation differs — the reply/resolve logic is identical.
export function CommentThread({thread, onChanged, inline = false}) {
    const [replying, setReplying] = useState(false);

    const reply = async body => {
        await api.addComment(thread.id, {body, author: 'user'});
        setReplying(false);
        await onChanged();
    };

    const setResolved = async resolved => {
        await api.resolveThread(thread.id, resolved);
        await onChanged();
    };

    const messages = thread.comments.map((c, idx) => (
        <div key={c.id} className="comment">
            <div className={`avatar ${inline ? 'avatar-sm' : 'avatar-md'} avatar-${c.author}`}>
                {c.author[0]}
            </div>
            <div className="comment-main">
                <div className="comment-meta">
                    <span className={`author author-${c.author}`}>{c.author}</span>
                    <span className="comment-time">{timeAgo(c.created_at)}</span>
                    {inline && idx === 0 && (
                        <>
                            <StatusPills thread={thread} />
                            {thread.file_path && (
                                <span className="comment-loc">:{thread.line} ({thread.side})</span>
                            )}
                        </>
                    )}
                </div>
                <div className="comment-body">{c.body}</div>
            </div>
        </div>
    ));

    const actions = replying ? (
        <CommentForm onSubmit={reply} onCancel={() => setReplying(false)} placeholder="Reply…" />
    ) : inline ? (
        <div className="thread-actions">
            <button className="act-text" onClick={() => setReplying(true)}>Reply</button>
            {thread.resolved
                ? <button className="act-text" onClick={() => setResolved(false)}>Unresolve</button>
                : <button className="act-text act-text-resolve" onClick={() => setResolved(true)}>Resolve</button>}
        </div>
    ) : (
        <div className="thread-actions">
            <button className="act-btn" onClick={() => setReplying(true)}>Reply</button>
            {thread.resolved
                ? <button className="act-btn" onClick={() => setResolved(false)}>Unresolve</button>
                : <button className="act-btn act-resolve" onClick={() => setResolved(true)}>Resolve</button>}
        </div>
    );

    if (inline) {
        return (
            <div className="thread-inline">
                {messages}
                {actions}
            </div>
        );
    }

    return (
        <div className="thread">
            <div className="thread-header">
                <span className="thread-label">PR comment</span>
                <span className="thread-statuses">
                    <StatusPills thread={thread} />
                </span>
            </div>
            <div className="thread-body">
                {messages}
                {actions}
            </div>
        </div>
    );
}
