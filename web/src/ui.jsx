import React from 'react';

export const STATE_LABELS = {
    all: 'All',
    open: 'Open',
    changes_requested: 'Changes requested',
    approved: 'Approved',
    merged: 'Merged',
    closed: 'Closed',
};

export function StateBadge({state}) {
    return <span className={`badge badge-${state}`}>{STATE_LABELS[state] || state}</span>;
}

export function timeAgo(iso) {
    const date = new Date(iso);
    const secs = Math.floor((Date.now() - date.getTime()) / 1000);
    if (secs < 60) return 'just now';
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    return date.toLocaleDateString();
}
