import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../design';
import './admin.css';
import { App } from './App';

// Admin is dark by default (design decisions, round 3); admin.html sets it before anything paints.
document.documentElement.classList.add('js');
const root = document.getElementById('root');
if (root) createRoot(root).render(<StrictMode><App /></StrictMode>);
