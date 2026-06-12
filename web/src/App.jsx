import React, {useEffect, useState} from 'react';
import Dashboard from './Dashboard.jsx';
import PRDetail from './PRDetail.jsx';

// Minimal hash router: '#/' = dashboard, '#/pr/<number>' = PR detail.
function parseHash() {
    const hash = window.location.hash.replace(/^#/, '') || '/';
    const m = hash.match(/^\/pr\/(\d+)/);
    if (m) return {page: 'pr', number: Number(m[1])};
    return {page: 'dashboard'};
}

export default function App() {
    const [route, setRoute] = useState(parseHash());

    useEffect(() => {
        const onChange = () => setRoute(parseHash());
        window.addEventListener('hashchange', onChange);
        return () => window.removeEventListener('hashchange', onChange);
    }, []);

    return (
        <div className="app">
            <header className="app-header">
                <a href="#/" className="brand">lgit</a>
                <span className="brand-sub">local PR review</span>
            </header>
            {route.page === 'pr'
                ? <PRDetail number={route.number} />
                : <Dashboard />}
        </div>
    );
}
