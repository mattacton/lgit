// Thin fetch wrappers over the lgit JSON API.

async function request(path, options = {}) {
    const res = await fetch(path, {
        headers: {'Content-Type': 'application/json'},
        ...options,
    });
    if (!res.ok) {
        let msg = `${res.status} ${res.statusText}`;
        try {
            const body = await res.json();
            if (body.error) msg = body.error;
        } catch {
            // keep default message
        }
        throw new Error(msg);
    }
    return res.json();
}

export const api = {
    listPRs: () => request('/api/prs'),
    getPR: number => request(`/api/prs/${number}`),
    getDiff: number => request(`/api/prs/${number}/diff`),
    listThreads: number => request(`/api/prs/${number}/threads`),
    createThread: (number, payload) =>
        request(`/api/prs/${number}/threads`, {method: 'POST', body: JSON.stringify(payload)}),
    createReview: (number, payload) =>
        request(`/api/prs/${number}/reviews`, {method: 'POST', body: JSON.stringify(payload)}),
    mergePR: (number, cleanup) =>
        request(`/api/prs/${number}/merge`, {method: 'POST', body: JSON.stringify({cleanup})}),
    addComment: (threadId, payload) =>
        request(`/api/threads/${threadId}/comments`, {method: 'POST', body: JSON.stringify(payload)}),
    resolveThread: (threadId, resolved) =>
        request(`/api/threads/${threadId}/resolve`, {method: 'POST', body: JSON.stringify({resolved})}),
};
