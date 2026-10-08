'use strict';
// Progressive enhancement only: navigation and downloads work without JavaScript.
document.querySelectorAll('a[href^="#"]').forEach(link=>link.addEventListener('click',()=>{link.blur()}));
