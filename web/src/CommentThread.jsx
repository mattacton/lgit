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
            {error && <div className="error-banner">{error}</div>}
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

export function CommentThread({thread, onChanged}) {
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

    return (
        <div className={thread.resolved ? 'thread thread-resolved' : 'thread'}>
            <div className="thread-header">
                <span className="thread-loc">
                    {thread.file_path
                        ? `${thread.file_path}:${thread.line} (${thread.side})`
                        : 'PR comment'}
                </span>
                <span className="thread-statuses">
                    {thread.outdated && <span className="thread-status thread-outdated">Outdated</span>}
                    <span className="thread-status">{thread.resolved ? 'Resolved' : 'Open'}</span>
                </span>
            </div>
            {thread.comments.map(c => (
                <div key={c.id} className="comment">
                    <div className="comment-meta">
                        <span className={`author author-${c.author}`}>{c.author}</span>
                        <span className="comment-time">{timeAgo(c.created_at)}</span>
                    </div>
                    <div className="comment-body">{c.body}</div>
                </div>
            ))}
            <div className="thread-actions">
                {replying ? (
                    <CommentForm onSubmit={reply} onCancel={() => setReplying(false)} placeholder="Reply…" />
                ) : (
                    <>
                        <button className="btn btn-small" onClick={() => setReplying(true)}>Reply</button>
                        {thread.resolved ? (
                            <button className="btn btn-small" onClick={() => setResolved(false)}>Unresolve</button>
                        ) : (
                            <button className="btn btn-small btn-resolve" onClick={() => setResolved(true)}>
                                Resolve
                            </button>
                        )}
                    </>
                )}
            </div>
        </div>
    );
}
