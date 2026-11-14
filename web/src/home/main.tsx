import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../design';
import './home.css';
import { App } from './App';

// Home follows the device's theme (design decisions, round 3): no data-theme on <html>.
document.documentElement.classList.add('js');
const root = document.getElementById('root');
if (root) createRoot(root).render(<StrictMode><App /></StrictMode>);
