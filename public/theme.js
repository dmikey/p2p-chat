'use strict';
(()=>{
 let preference='auto';try{const saved=localStorage.getItem('radchat-theme');if(['auto','day','night'].includes(saved))preference=saved}catch{}
 function apply(){const hour=new Date().getHours();const theme=preference==='auto'?(hour>=7&&hour<19?'day':'night'):preference;document.documentElement.dataset.theme=theme;document.documentElement.style.colorScheme=theme==='night'?'dark':'light';document.querySelectorAll('.theme-switch').forEach(b=>{b.textContent=(theme==='day'?'☀ ':'☾ ')+(preference==='auto'?'Auto':preference==='day'?'Day':'Night');b.title='Theme: '+preference+'. Click to switch between Auto, Day and Night.';b.setAttribute('aria-label',b.title)});const meta=document.querySelector('meta[name="theme-color"]');if(meta)meta.content=theme==='day'?'#f8f5e9':'#151126'}
 apply();document.addEventListener('DOMContentLoaded',()=>{document.querySelectorAll('.theme-switch').forEach(b=>b.addEventListener('click',()=>{preference={auto:'day',day:'night',night:'auto'}[preference];try{localStorage.setItem('radchat-theme',preference)}catch{}apply()}));apply()});setInterval(apply,60000);document.addEventListener('visibilitychange',apply);
})();
