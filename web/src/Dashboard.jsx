import React, {useEffect, useState} from 'react';
import {api} from './api.js';
import {StateBadge, STATE_LABELS, timeAgo} from './ui.jsx';

const STATES = ['all', 'open', 'changes_requested', 'approved', 'merged', 'closed'];

export default function Dashboard() {
    const [prs, setPRs] = useState(null);
    const [error, setError] = useState(null);
    const [filter, setFilter] = useState('all');

    useEffect(() => {
        api.listPRs().then(setPRs).catch(e => setError(e.message));
    }, []);

    if (error) return <div className="banner banner-error">{error}</div>;
    if (!prs) return <div className="loading">Loading…</div>;

    const shown = filter === 'all' ? prs : prs.filter(p => p.state === filter);
    const openCount = prs.filter(p => p.state === 'open' || p.state === 'changes_requested').length;

    return (
        <main className="dashboard">
            <div className="dashboard-header">
                <div>
                    <h1 className="dashboard-title">Pull requests</h1>
                    <div className="dashboard-sub">{openCount} open · {prs.length} total</div>
                </div>
                <div className="state-filter">
                    {STATES.map(s => (
                        <button
                            key={s}
                            className={filter === s ? 'chip chip-active' : 'chip'}
                            onClick={() => setFilter(s)}
                        >
                            {STATE_LABELS[s]}
                        </button>
                    ))}
                </div>
            </div>
            {shown.length === 0 && <p className="empty">No PRs. Create one with <code>lgit pr create</code>.</p>}
            <ul className="pr-list">
                {shown.map(pr => (
                    <li key={pr.id}>
                        <a className="pr-row" href={`#/pr/${pr.number}`}>
                            <span className="pr-number">#{pr.number}</span>
                            <div className="pr-main">
                                <div className="pr-title">{pr.title}</div>
                                <div className="pr-meta">
                                    <span className="pr-branch">{pr.head_ref}</span>{' '}
                                    <span className="pr-meta-rest">→ {pr.base_ref} · {timeAgo(pr.created_at)}</span>
                                </div>
                            </div>
                            <StateBadge state={pr.state} />
                        </a>
                    </li>
                ))}
            </ul>
        </main>
    );
}
