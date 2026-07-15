import '../shared/fonts.js';
import '../styles.css';
import '../docs.css';

import { createRoot } from 'react-dom/client';
import { DocsApp } from './docs.jsx';

createRoot(document.getElementById('root')).render(<DocsApp />);
