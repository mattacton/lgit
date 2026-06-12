import React, {useEffect, useState} from 'react';
import {api} from './api.js';
import {StateBadge} from './ui.jsx';

export default function Dashboard() {
    const [prs, setPRs] = useState(null);
    const [error, setError] = useState(null);
    const [filter, setFilter] = useState('all');

    useEffect(() => {
        api.listPRs().then(setPRs).catch(e => setError(e.message));
    }, []);

    if (error) return <div className="error-banner">{error}</div>;
    if (!prs) return <div className="loading">Loading…</div>;

    const shown = filter === 'all' ? prs : prs.filter(p => p.state === filter);
    const states = ['all', 'open', 'changes_requested', 'approved', 'merged', 'closed'];

    return (
        <main className="dashboard">
            <div className="dashboard-toolbar">
                <h1>Pull requests</h1>
                <div className="state-filter">
                    {states.map(s => (
                        <button
                            key={s}
                            className={filter === s ? 'chip chip-active' : 'chip'}
                            onClick={() => setFilter(s)}
                        >
                            {s.replace('_', ' ')}
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
                            <span className="pr-title">{pr.title}</span>
                            <span className="pr-branches">{pr.head_ref} → {pr.base_ref}</span>
                            <StateBadge state={pr.state} />
                        </a>
                    </li>
                ))}
            </ul>
        </main>
    );
}
