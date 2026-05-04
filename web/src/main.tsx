import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './index.css';
import { App } from './App';

// Track the *visual* viewport height in a CSS variable so the app can shrink
// when the iOS soft keyboard appears. window.innerHeight / 100vh / 100dvh
// all keep the layout viewport at full height behind the keyboard, leaving
// the bottom of the terminal hidden until the user scrolls. VisualViewport
// reports the area that's actually visible to the user.
function syncViewportHeight() {
  const vv = window.visualViewport;
  const h = vv ? vv.height : window.innerHeight;
  const offsetTop = vv ? vv.offsetTop : 0;
  document.documentElement.style.setProperty('--app-height', `${h}px`);
  document.documentElement.style.setProperty('--app-offset-top', `${offsetTop}px`);
  // Detect the iOS / Android soft keyboard. visualViewport.height shrinks
  // by the keyboard's height; window.innerHeight stays the same. A 150px
  // delta is well above the rounding noise from URL-bar collapse.
  const kbdUp = !!vv && window.innerHeight - h > 150;
  document.documentElement.classList.toggle('kbd-up', kbdUp);
}
syncViewportHeight();
if (window.visualViewport) {
  window.visualViewport.addEventListener('resize', syncViewportHeight);
  window.visualViewport.addEventListener('scroll', syncViewportHeight);
} else {
  window.addEventListener('resize', syncViewportHeight);
}

const root = document.getElementById('root');
if (!root) throw new Error('Root element not found');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>
);
