'use strict';
(()=>{
 // Migrate the former Auto setting to a single initial choice. There are only two modes.
 let preference=new Date().getHours()>=7&&new Date().getHours()<19?'day':'night';
 try{const saved=localStorage.getItem('radchat-theme');if(['day','night'].includes(saved))preference=saved}catch{}
 function apply(){document.documentElement.dataset.theme=preference;document.documentElement.style.colorScheme=preference==='night'?'dark':'light';document.querySelectorAll('.theme-switch').forEach(b=>{const light=preference==='day';b.textContent=light?'☀ Light':'☾ Dark';b.title=(light?'Light':'Dark')+' mode. Switch to '+(light?'dark':'light')+' mode.';b.setAttribute('aria-label',b.title)});const meta=document.querySelector('meta[name="theme-color"]');if(meta)meta.content=preference==='day'?'#f8f5e9':'#151126'}
 apply();document.addEventListener('DOMContentLoaded',()=>{document.querySelectorAll('.theme-switch').forEach(b=>b.addEventListener('click',()=>{preference=preference==='day'?'night':'day';try{localStorage.setItem('radchat-theme',preference)}catch{}apply()}));apply()});
})();
