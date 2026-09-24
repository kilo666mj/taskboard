importScripts('/pwa-kit/worker.js');
const CACHE='taskboard-v9';
const ASSETS=['/','/app.css','/app.js','/pwa-kit/browser.js','/manifest.webmanifest','/icons/icon-192.png','/icons/icon-512.png'];
self.addEventListener('install',event=>event.waitUntil(caches.open(CACHE).then(cache=>cache.addAll(ASSETS)).then(()=>self.skipWaiting())));
self.addEventListener('activate',event=>event.waitUntil(caches.keys().then(keys=>Promise.all(keys.filter(key=>key!==CACHE).map(key=>caches.delete(key)))).then(()=>self.clients.claim())));
self.addEventListener('fetch',event=>{if(event.request.method!=='GET'||new URL(event.request.url).pathname.startsWith('/api/'))return;event.respondWith(fetch(event.request).then(response=>{const copy=response.clone();caches.open(CACHE).then(cache=>cache.put(event.request,copy));return response}).catch(()=>caches.match(event.request).then(hit=>hit||caches.match('/'))))});
PWAKitWorker.installPushHandlers({title:'Taskboard updated',icon:'/icons/icon-192.png',badge:'/icons/icon-192.png',tag:'taskboard-update',notificationOptions:data=>({requireInteraction:Boolean(data.urgent)})});
