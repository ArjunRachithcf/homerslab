import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../../design';
import '../../setup/setup.css';
import { Account } from './Account';

document.documentElement.classList.add('js');
const root = document.getElementById('root');
if (root) createRoot(root).render(<StrictMode><Account /></StrictMode>);
