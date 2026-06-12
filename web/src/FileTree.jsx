import React from 'react';

// Builds a nested tree from file paths.
function buildTree(files) {
    const root = {dirs: new Map(), files: []};
    for (const f of files) {
        const path = f.new_path || f.old_path;
        const parts = path.split('/');
        let node = root;
        for (const part of parts.slice(0, -1)) {
            if (!node.dirs.has(part)) node.dirs.set(part, {dirs: new Map(), files: []});
            node = node.dirs.get(part);
        }
        node.files.push({...f, path, name: parts[parts.length - 1]});
    }
    return root;
}

function fileAnchor(path) {
    return `file-${path.replace(/[^a-zA-Z0-9_-]/g, '_')}`;
}

function TreeLevel({node, depth, viewed, threadsByFile}) {
    const indent = {paddingLeft: `${depth * 14}px`};
    return (
        <>
            {[...node.dirs.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, child]) => (
                <React.Fragment key={name}>
                    <div className="tree-dir" style={indent}>{name}/</div>
                    <TreeLevel node={child} depth={depth + 1} viewed={viewed} threadsByFile={threadsByFile} />
                </React.Fragment>
            ))}
            {node.files.sort((a, b) => a.name.localeCompare(b.name)).map(f => {
                const threadCount = (threadsByFile.get(f.path) || []).filter(t => !t.resolved).length;
                return (
                    <a key={f.path} className="tree-file" style={indent} href={`#${window.location.hash.slice(1).split('?')[0]}`}
                        onClick={e => {
                            e.preventDefault();
                            document.getElementById(fileAnchor(f.path))?.scrollIntoView({behavior: 'smooth'});
                        }}
                    >
                        <span className={`tree-name status-${f.status}`}>{f.name}</span>
                        <span className="tree-meta">
                            {threadCount > 0 && <span className="tree-comments">{threadCount}💬</span>}
                            <span className="add">+{f.additions}</span>
                            <span className="del">−{f.deletions}</span>
                            {viewed[f.path] && <span className="tree-viewed">✓</span>}
                        </span>
                    </a>
                );
            })}
        </>
    );
}

export default function FileTree({files, viewed, threadsByFile}) {
    const tree = buildTree(files);
    return (
        <nav className="file-tree">
            <TreeLevel node={tree} depth={0} viewed={viewed} threadsByFile={threadsByFile} />
        </nav>
    );
}

export {fileAnchor};
