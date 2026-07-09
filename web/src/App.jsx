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

function initialTheme() {
    try {
        return localStorage.getItem('lgit:theme') === 'light' ? 'light' : 'dark';
    } catch {
        return 'dark';
    }
}

export default function App() {
    const [route, setRoute] = useState(parseHash());
    const [theme, setTheme] = useState(initialTheme);

    useEffect(() => {
        const onChange = () => setRoute(parseHash());
        window.addEventListener('hashchange', onChange);
        return () => window.removeEventListener('hashchange', onChange);
    }, []);

    useEffect(() => {
        document.documentElement.dataset.theme = theme;
        try {
            localStorage.setItem('lgit:theme', theme);
        } catch {
            // persistence is best-effort
        }
    }, [theme]);

    const toggleTheme = () => setTheme(t => (t === 'dark' ? 'light' : 'dark'));

    return (
        <div className="app">
            <header className="app-header">
                <a href="#/" className="brand">
                    <span className="brand-mark">›</span>
                    <span className="brand-name">lgit</span>
                    <span className="brand-sub">local PR review</span>
                </a>
                <div className="header-right">
                    <button
                        className="theme-toggle"
                        onClick={toggleTheme}
                        title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
                    >
                        {theme === 'dark' ? '☾' : '☀'} {theme}
                    </button>
                    <span className="conn">
                        <span className="conn-dot" />
                        <span className="conn-label">connected</span>
                    </span>
                </div>
            </header>
            {route.page === 'pr'
                ? <PRDetail number={route.number} />
                : <Dashboard />}
        </div>
    );
}
