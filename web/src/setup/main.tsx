import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../design';
import './setup.css';
import { App } from './App';

document.documentElement.classList.add('js');
const root = document.getElementById('root');
if (root) createRoot(root).render(<StrictMode><App /></StrictMode>);
