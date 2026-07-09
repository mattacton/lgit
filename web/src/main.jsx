import React from 'react';
import {createRoot} from 'react-dom/client';
// Self-hosted IBM Plex, latin subset only (keeps the go:embed'd dist small).
import '@fontsource/ibm-plex-sans/latin-400.css';
import '@fontsource/ibm-plex-sans/latin-500.css';
import '@fontsource/ibm-plex-sans/latin-600.css';
import '@fontsource/ibm-plex-mono/latin-400.css';
import '@fontsource/ibm-plex-mono/latin-500.css';
import '@fontsource/ibm-plex-mono/latin-600.css';
// react-diff-view ships its own :root { --diff-* } defaults; this import MUST
// stay before styles.css so our overrides in styles.css win the cascade.
import 'react-diff-view/style/index.css';
import './styles.css';
import App from './App.jsx';

createRoot(document.getElementById('root')).render(<App />);
