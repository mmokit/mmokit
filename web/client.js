import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import {
    ClientMessageSchema,
    ServerMessageSchema,
    PlayerInputMsgSchema,
    RespawnRequestMsgSchema,
    LoginMsgSchema,
    ChatMsgSchema,
    EntityType,
    ResourceType,
} from "/gen/es/game_pb.js";
import { WSTransport } from "/transport.js";

const canvas = document.getElementById('game');
const ctx = canvas.getContext('2d');
const hud = document.getElementById('hud');
const statusEl = document.getElementById('status');

// Resize canvas to fill window
function resize() {
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
}
window.addEventListener('resize', resize);
resize();

// Login state
let playerUsername = '';
let loggedIn = false;
let loginInput = localStorage.getItem('username') || '';
let loginCursorVisible = true;
let loginCursorTimer = 0;
let loginActive = true;
let loginError = ''; // rejection reason to display on login screen

// Game state
let ws = null;
let myEntityId = 0;
let worldWidth = 10000;
let worldHeight = 10000;
let cameraX = 0;
let cameraY = 0;
let connected = false;
let inputSeq = 0;
let tickCount = 0;
let fps = 0;
let lastFpsTime = performance.now();
let frameCount = 0;

// Interpolation state
let entities = new Map();
let lastTickTime = 0;
const TICK_INTERVAL = 50; // 20Hz = 50ms

// Death/respawn state
let isDead = false;
let deathTime = 0;
let killerEntityId = 0;

// Targeting state
let targetId = 0;
let mouseX = 0;
let mouseY = 0;

// Cargo panel state
let cargoPanelOpen = false;
let jettisonRequest = 0; // 1-4 = jettison that resource type, 0 = none
const MAX_CARGO = 100;

// Economy state
let sellRequest = false;
let sellPrices = [1, 3, 2, 5];
let playerFlux = 0;
let toasts = []; // {text, time}

// Thruster particles (per-entity, stored on ent object as ent.thrusterParticles)
const MAX_THRUSTER_PARTICLES = 20;
let lastThrusterTime = 0;

function updateThrusterParticles(ent, isThrusting, dt) {
    if (!ent.thrusterParticles) ent.thrusterParticles = [];
    const particles = ent.thrusterParticles;
    const e = ent.curr;
    const hw = (e.width || 60) / 2;
    const hh = (e.height || 30) / 2;
    const rot = ent.renderRot;

    // Spawn new particles when thrusting
    if (isThrusting && particles.length < MAX_THRUSTER_PARTICLES) {
        // Two emitter points (top and bottom engine nozzles)
        for (const nozzleOff of [-hh * 0.3, hh * 0.3]) {
            // Nozzle position in world space
            const localX = -hw * 0.72;
            const localY = nozzleOff;
            const wx = ent.renderX + Math.cos(rot) * localX - Math.sin(rot) * localY;
            const wy = ent.renderY + Math.sin(rot) * localX + Math.cos(rot) * localY;

            // Emit backward (opposite of ship facing) with spread
            const spread = (Math.random() - 0.5) * 0.6;
            const emitAngle = rot + Math.PI + spread;
            const speed = 60 + Math.random() * 120;

            particles.push({
                x: wx + (Math.random() - 0.5) * 3,
                y: wy + (Math.random() - 0.5) * 3,
                vx: Math.cos(emitAngle) * speed,
                vy: Math.sin(emitAngle) * speed,
                life: 0.2 + Math.random() * 0.3,
                maxLife: 0,
                size: 1.5 + Math.random() * 2.5,
            });
            particles[particles.length - 1].maxLife = particles[particles.length - 1].life;
        }
    }

    // Update existing particles
    for (let i = particles.length - 1; i >= 0; i--) {
        const p = particles[i];
        p.x += p.vx * dt;
        p.y += p.vy * dt;
        p.vx *= 0.94;
        p.vy *= 0.94;
        p.life -= dt;
        if (p.life <= 0) {
            particles.splice(i, 1);
        }
    }
}

function drawThrusterParticles(ent, isMe) {
    if (!ent.thrusterParticles) return;
    for (const p of ent.thrusterParticles) {
        const t = 1 - (p.life / p.maxLife);
        const alpha = (1 - t * t) * 0.8;
        const sx = p.x - cameraX;
        const sy = p.y - cameraY;
        const size = p.size * (1 - t * 0.5);

        // White-hot core fading to ion blue
        const r = Math.floor(200 * (1 - t * 0.8));
        const g = Math.floor(220 * (1 - t * 0.5));
        const b = 255;

        ctx.beginPath();
        ctx.arc(sx, sy, size, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${alpha})`;
        ctx.shadowColor = `rgba(100, 160, 255, ${alpha * 0.5})`;
        ctx.shadowBlur = 3;
        ctx.fill();
        ctx.shadowBlur = 0;
    }
}

// Death explosions
let explosions = []; // active explosion effects

function spawnExplosion(worldX, worldY, shipW, shipH, isMe) {
    const now = performance.now();
    const size = Math.max(shipW || 60, shipH || 30);
    const particles = [];

    // Debris chunks — angular pieces that spin and fade
    for (let i = 0; i < 18; i++) {
        const angle = Math.random() * Math.PI * 2;
        const speed = 40 + Math.random() * 160;
        particles.push({
            type: 'debris',
            x: (Math.random() - 0.5) * size * 0.3,
            y: (Math.random() - 0.5) * size * 0.3,
            vx: Math.cos(angle) * speed,
            vy: Math.sin(angle) * speed,
            rot: Math.random() * Math.PI * 2,
            rotSpeed: (Math.random() - 0.5) * 12,
            w: 3 + Math.random() * 8,
            h: 2 + Math.random() * 4,
            life: 0.6 + Math.random() * 0.9,
            maxLife: 0,
            color: isMe ? [0, 255, 0] : [68 + Math.random() * 60, 170 + Math.random() * 50, 255],
        });
        particles[particles.length - 1].maxLife = particles[particles.length - 1].life;
    }

    // Hot sparks — tiny bright dots that scatter fast
    for (let i = 0; i < 30; i++) {
        const angle = Math.random() * Math.PI * 2;
        const speed = 80 + Math.random() * 250;
        particles.push({
            type: 'spark',
            x: (Math.random() - 0.5) * size * 0.15,
            y: (Math.random() - 0.5) * size * 0.15,
            vx: Math.cos(angle) * speed,
            vy: Math.sin(angle) * speed,
            life: 0.3 + Math.random() * 0.5,
            maxLife: 0,
            size: 1 + Math.random() * 2,
        });
        particles[particles.length - 1].maxLife = particles[particles.length - 1].life;
    }

    // Flame puffs — expanding glowing circles
    for (let i = 0; i < 8; i++) {
        const angle = Math.random() * Math.PI * 2;
        const speed = 15 + Math.random() * 60;
        particles.push({
            type: 'flame',
            x: (Math.random() - 0.5) * size * 0.2,
            y: (Math.random() - 0.5) * size * 0.2,
            vx: Math.cos(angle) * speed,
            vy: Math.sin(angle) * speed,
            life: 0.4 + Math.random() * 0.6,
            maxLife: 0,
            radius: 4 + Math.random() * 10,
        });
        particles[particles.length - 1].maxLife = particles[particles.length - 1].life;
    }

    explosions.push({
        x: worldX,
        y: worldY,
        startTime: now,
        particles,
        // Shockwave ring
        shockRadius: size * 0.2,
        shockMaxRadius: size * 1.8,
        shockDuration: 0.5,
        // Initial flash
        flashDuration: 0.15,
        flashRadius: size * 0.8,
        duration: 1.8,
    });
}

function updateAndDrawExplosions(now) {
    const dt = 1 / 60; // approximate frame dt

    for (let i = explosions.length - 1; i >= 0; i--) {
        const ex = explosions[i];
        const elapsed = (now - ex.startTime) / 1000;

        if (elapsed > ex.duration) {
            explosions.splice(i, 1);
            continue;
        }

        const sx = ex.x - cameraX;
        const sy = ex.y - cameraY;

        // Skip if offscreen (generous margin for shockwave)
        if (sx < -300 || sx > canvas.width + 300 || sy < -300 || sy > canvas.height + 300) continue;

        // Flash — bright white/orange glow at start
        if (elapsed < ex.flashDuration) {
            const flashT = elapsed / ex.flashDuration;
            const flashAlpha = (1 - flashT) * 0.9;
            const r = ex.flashRadius * (0.5 + flashT * 0.5);
            const grad = ctx.createRadialGradient(sx, sy, 0, sx, sy, r);
            grad.addColorStop(0, `rgba(255, 220, 160, ${flashAlpha})`);
            grad.addColorStop(0.3, `rgba(255, 140, 40, ${flashAlpha * 0.7})`);
            grad.addColorStop(1, `rgba(255, 60, 10, 0)`);
            ctx.fillStyle = grad;
            ctx.beginPath();
            ctx.arc(sx, sy, r, 0, Math.PI * 2);
            ctx.fill();
        }

        // Shockwave ring
        if (elapsed < ex.shockDuration) {
            const shockT = elapsed / ex.shockDuration;
            const r = ex.shockRadius + (ex.shockMaxRadius - ex.shockRadius) * shockT;
            const alpha = (1 - shockT) * 0.6;
            ctx.beginPath();
            ctx.arc(sx, sy, r, 0, Math.PI * 2);
            ctx.strokeStyle = `rgba(255, 180, 80, ${alpha})`;
            ctx.lineWidth = 2 + (1 - shockT) * 3;
            ctx.shadowColor = `rgba(255, 120, 20, ${alpha * 0.8})`;
            ctx.shadowBlur = 10;
            ctx.stroke();
            ctx.shadowBlur = 0;
        }

        // Particles
        for (const p of ex.particles) {
            if (p.life <= 0) continue;

            p.x += p.vx * dt;
            p.y += p.vy * dt;
            p.vx *= 0.97; // drag
            p.vy *= 0.97;
            p.life -= dt;

            const t = 1 - (p.life / p.maxLife); // 0 -> 1 as it dies
            const alpha = Math.max(0, 1 - t * t); // ease out
            const px = sx + p.x;
            const py = sy + p.y;

            if (p.type === 'debris') {
                p.rot += p.rotSpeed * dt;
                ctx.save();
                ctx.translate(px, py);
                ctx.rotate(p.rot);
                // Shift from ship color toward dark orange/red as it cools
                const r = Math.floor(p.color[0] + (200 - p.color[0]) * t * 0.5);
                const g = Math.floor(p.color[1] * (1 - t * 0.7));
                const b = Math.floor(p.color[2] * (1 - t * 0.9));
                ctx.fillStyle = `rgba(${r}, ${g}, ${b}, ${alpha})`;
                ctx.fillRect(-p.w / 2, -p.h / 2, p.w, p.h);
                // Hot edge glow on fresh debris
                if (t < 0.4) {
                    ctx.shadowColor = `rgba(255, 160, 40, ${(0.4 - t) * 2})`;
                    ctx.shadowBlur = 6;
                    ctx.strokeStyle = `rgba(255, 200, 100, ${(0.4 - t) * 1.5})`;
                    ctx.lineWidth = 1;
                    ctx.strokeRect(-p.w / 2, -p.h / 2, p.w, p.h);
                    ctx.shadowBlur = 0;
                }
                ctx.restore();
            } else if (p.type === 'spark') {
                // White-hot to orange to red
                const r2 = 255;
                const g2 = Math.floor(255 * (1 - t * 0.8));
                const b2 = Math.floor(200 * (1 - t));
                ctx.fillStyle = `rgba(${r2}, ${g2}, ${b2}, ${alpha})`;
                ctx.shadowColor = `rgba(${r2}, ${g2}, 50, ${alpha * 0.6})`;
                ctx.shadowBlur = 4;
                ctx.beginPath();
                ctx.arc(px, py, p.size * (1 - t * 0.5), 0, Math.PI * 2);
                ctx.fill();
                ctx.shadowBlur = 0;
            } else if (p.type === 'flame') {
                const r3 = p.radius * (1 + t * 2); // expands
                const flameAlpha = alpha * 0.5;
                const grad = ctx.createRadialGradient(px, py, 0, px, py, r3);
                grad.addColorStop(0, `rgba(255, 200, 60, ${flameAlpha})`);
                grad.addColorStop(0.5, `rgba(255, 80, 10, ${flameAlpha * 0.5})`);
                grad.addColorStop(1, `rgba(100, 20, 0, 0)`);
                ctx.fillStyle = grad;
                ctx.beginPath();
                ctx.arc(px, py, r3, 0, Math.PI * 2);
                ctx.fill();
            }
        }
    }
}

// Chat state
let chatMode = false;
const chatMessagesEl = document.getElementById('chat-messages');
const chatInputEl = document.getElementById('chat-input');
const MAX_CHAT_DISPLAY = 50;

// Input state
const keys = {};
window.addEventListener('keydown', (e) => {
    if (loginActive) return;

    // Chat mode handling
    if (chatMode) {
        if (e.code === 'Escape') {
            chatMode = false;
            chatInputEl.style.display = 'none';
            chatInputEl.value = '';
        } else if (e.code === 'Enter') {
            const text = chatInputEl.value.trim();
            if (text && connected && ws) {
                ws.sendReliable(encodeChatMessage(text));
            }
            chatMode = false;
            chatInputEl.style.display = 'none';
            chatInputEl.value = '';
        }
        return; // suppress all other input while chatting
    }

    keys[e.code] = true;

    if (e.code === 'Enter' && !isDead) {
        chatMode = true;
        chatInputEl.style.display = 'block';
        chatInputEl.focus();
        return;
    }

    if (isDead && (e.code === 'Space' || e.code === 'Enter')) {
        if (connected && ws) {
            ws.sendReliable(encodeRespawnRequest());
        }
    }
    if (e.code === 'KeyC' && !isDead) {
        cargoPanelOpen = !cargoPanelOpen;
    }
    if (e.code === 'KeyE' && !isDead) {
        sellRequest = true;
    }
    // 1-4 keys jettison cargo while panel is open
    if (cargoPanelOpen) {
        const digit = parseInt(e.key);
        if (digit >= 1 && digit <= 4) {
            jettisonRequest = digit;
        }
    }
});
window.addEventListener('keyup', (e) => { if (!loginActive && !chatMode) keys[e.code] = false; });

// Mouse tracking for targeting
canvas.addEventListener('mousemove', (e) => {
    mouseX = e.clientX;
    mouseY = e.clientY;
});

canvas.addEventListener('click', (e) => {
    if (isDead) return;
    const clickWorldX = e.clientX + cameraX;
    const clickWorldY = e.clientY + cameraY;

    let bestId = 0;
    let bestDist = Infinity;

    for (const [id, ent] of entities) {
        if (ent.curr.entityType !== EntityType.ASTEROID && ent.curr.entityType !== EntityType.LOOT_CRATE) continue;
        const dx = ent.renderX - clickWorldX;
        const dy = ent.renderY - clickWorldY;
        const dist = Math.sqrt(dx * dx + dy * dy);
        const hitRadius = (ent.curr.radius || 20) + 10; // small tolerance
        if (dist < hitRadius && dist < bestDist) {
            bestDist = dist;
            bestId = id;
        }
    }

    targetId = bestId;
});

// === Protocol ===

function encodePlayerInput(thrust, turn, fire, mine, sequence, targetIdVal, jettison, sell) {
    const input = create(PlayerInputMsgSchema, { thrust, turn, fire, mine, sequence, targetId: targetIdVal, jettison, sell });
    const msg = create(ClientMessageSchema, { msg: { case: "input", value: input } });
    return toBinary(ClientMessageSchema, msg);
}

function encodeRespawnRequest() {
    const respawn = create(RespawnRequestMsgSchema, {});
    const msg = create(ClientMessageSchema, { msg: { case: "respawn", value: respawn } });
    return toBinary(ClientMessageSchema, msg);
}

function encodeLogin(username) {
    const login = create(LoginMsgSchema, { username });
    const msg = create(ClientMessageSchema, { msg: { case: "login", value: login } });
    return toBinary(ClientMessageSchema, msg);
}

function encodeChatMessage(text) {
    const chat = create(ChatMsgSchema, { text });
    const msg = create(ClientMessageSchema, { msg: { case: "chat", value: chat } });
    return toBinary(ClientMessageSchema, msg);
}

function decodeServerMessage(data) {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    return fromBinary(ServerMessageSchema, bytes);
}

// === Interpolation ===

function lerp(a, b, t) {
    return a + (b - a) * t;
}

function lerpAngle(a, b, t) {
    let diff = b - a;
    while (diff > Math.PI) diff -= Math.PI * 2;
    while (diff < -Math.PI) diff += Math.PI * 2;
    return a + diff * t;
}

function updateEntityFromServer(serverState) {
    const id = serverState.id;
    let ent = entities.get(id);

    if (!ent) {
        ent = {
            prev: { ...serverState },
            curr: { ...serverState },
            renderX: serverState.x,
            renderY: serverState.y,
            renderRot: serverState.rotation,
        };
        entities.set(id, ent);
    } else {
        ent.prev = ent.curr;
        ent.curr = { ...serverState };
    }
}

function interpolateEntities(t) {
    for (const [id, ent] of entities) {
        const p = ent.prev;
        const c = ent.curr;

        if (t <= 1.0) {
            ent.renderX = lerp(p.x, c.x, t);
            ent.renderY = lerp(p.y, c.y, t);
            ent.renderRot = lerpAngle(p.rotation, c.rotation, t);
        } else {
            const dt = (t - 1.0) * TICK_INTERVAL / 1000;
            ent.renderX = c.x + c.vx * dt;
            ent.renderY = c.y + c.vy * dt;
            ent.renderRot = c.rotation;
        }
    }
}

// === WebSocket Connection ===

let spawnedOnce = false; // true after first successful spawn

function connect() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WSTransport(`${protocol}//${window.location.host}/ws`);

    ws.onOpen(() => {
        connected = true;
        statusEl.textContent = 'Connected - Logging in...';
        statusEl.style.color = '#0f0';
        ws.sendReliable(encodeLogin(playerUsername));
    });

    ws.onClose(() => {
        connected = false;
        // Only auto-reconnect if we successfully spawned before.
        // If we never spawned, either login was rejected or failed —
        // return to the login screen instead of looping.
        if (!spawnedOnce) {
            if (!loginError) loginError = 'Connection lost';
            loggedIn = false;
            loginActive = true;
            myEntityId = 0;
            entities.clear();
            window.addEventListener('keydown', handleLoginKeydown);
            requestAnimationFrame(renderLoginScreen);
            return;
        }
        statusEl.textContent = 'Disconnected - Reconnecting...';
        statusEl.style.color = '#f00';
        myEntityId = 0;
        entities.clear();
        setTimeout(connect, 2000);
    });

    ws.onMessage((rawData) => {
        const msg = decodeServerMessage(rawData);
        const inner = msg.msg;
        if (!inner) return;

        switch (inner.case) {
            case "playerSpawned": {
                const spawned = inner.value;
                myEntityId = spawned.yourEntityId;
                worldWidth = spawned.worldWidth;
                worldHeight = spawned.worldHeight;
                if (spawned.sellPrices && spawned.sellPrices.length === 4) {
                    sellPrices = [...spawned.sellPrices];
                }
                isDead = false;
                playerFlux = 0;
                spawnedOnce = true;
                entities.clear(); // Clear stale entities from before death
                statusEl.textContent = `Connected (ID: ${myEntityId})`;
                break;
            }

            case "worldUpdate": {
                const update = inner.value;
                tickCount = update.tick;
                lastTickTime = performance.now();

                for (const e of update.entities) {
                    updateEntityFromServer(e);
                }
                for (const id of update.removedIds) {
                    const removed = entities.get(id);
                    if (removed && removed.curr.entityType === EntityType.SHIP) {
                        // Ship destroyed — spawn explosion at its last position
                        spawnExplosion(
                            removed.renderX, removed.renderY,
                            removed.curr.width, removed.curr.height,
                            id === myEntityId
                        );
                    }
                    entities.delete(id);
                    if (id === targetId) targetId = 0;
                }
                // Display chat messages
                if (update.chatMessages) {
                    for (const chat of update.chatMessages) {
                        const div = document.createElement('div');
                        div.textContent = `[${chat.username}]: ${chat.text}`;
                        chatMessagesEl.appendChild(div);
                    }
                    // Cap displayed messages
                    while (chatMessagesEl.childElementCount > MAX_CHAT_DISPLAY) {
                        chatMessagesEl.removeChild(chatMessagesEl.firstChild);
                    }
                    // Auto-scroll to bottom
                    chatMessagesEl.scrollTop = chatMessagesEl.scrollHeight;
                }
                break;
            }

            case "sellResult": {
                const result = inner.value;
                playerFlux = result.totalFlux;
                toasts.push({ text: `+${result.fluxEarned.toFixed(0)} FLUX`, time: performance.now() });
                break;
            }

            case "playerDied": {
                const died = inner.value;
                isDead = true;
                deathTime = performance.now();
                killerEntityId = died.killerId;
                targetId = 0;
                cargoPanelOpen = false;
                const myEnt = entities.get(myEntityId);
                if (myEnt) {
                    spawnExplosion(myEnt.renderX, myEnt.renderY, myEnt.curr.width, myEnt.curr.height, true);
                }
                entities.delete(myEntityId);
                myEntityId = 0;
                break;
            }

            case "loginRejected": {
                const rejected = inner.value;
                loginError = rejected.reason || 'Login rejected';
                // Close triggers onClose which returns to login screen
                if (ws) ws.close();
                break;
            }
        }
    });
}

// === Input Sending ===

function sendInput() {
    if (!connected || !ws) return;
    if (isDead || chatMode) return;

    let thrust = 0;
    let turn = 0;
    const fire = !!(keys['Space']);
    const mine = !!(keys['KeyF']);

    if (keys['KeyW'] || keys['ArrowUp']) thrust = 1;
    if (keys['KeyS'] || keys['ArrowDown']) thrust = -1;
    if (keys['KeyA'] || keys['ArrowLeft']) turn = -1;
    if (keys['KeyD'] || keys['ArrowRight']) turn = 1;

    inputSeq++;
    const jett = jettisonRequest;
    jettisonRequest = 0;
    const sell = sellRequest;
    sellRequest = false;
    const data = encodePlayerInput(thrust, turn, fire, mine, inputSeq, targetId, jett, sell);
    ws.sendUnreliable(data);
}

setInterval(sendInput, 50);

// === Rendering ===

const ENTITY_COLORS = {
    [EntityType.SHIP]: '#4af',
    [EntityType.ASTEROID]: '#a86', // fallback, overridden by resource color
    [EntityType.PROJECTILE]: '#ff4',
    [EntityType.STATION]: '#8f8',
};

const RESOURCE_COLORS = ['#c90', '#a4f', '#4df', '#aaa']; // Ore, Crystal, Gas, Metal
const RESOURCE_NAMES = ['Ore', 'Crystal', 'Gas', 'Metal'];

// Pre-generate starfield layers (parallax)
const starLayers = [];
{
    const layerConfigs = [
        { count: 200, parallax: 0.05, sizeMin: 0.5, sizeMax: 1.0, alphaMin: 0.15, alphaMax: 0.35 },
        { count: 120, parallax: 0.15, sizeMin: 0.8, sizeMax: 1.5, alphaMin: 0.25, alphaMax: 0.5 },
        { count: 60,  parallax: 0.3,  sizeMin: 1.0, sizeMax: 2.0, alphaMin: 0.4,  alphaMax: 0.7 },
    ];
    // Seeded pseudo-random for deterministic star positions
    function mulberry32(a) {
        return function() {
            a |= 0; a = a + 0x6D2B79F5 | 0;
            let t = Math.imul(a ^ a >>> 15, 1 | a);
            t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
            return ((t ^ t >>> 14) >>> 0) / 4294967296;
        };
    }
    const rng = mulberry32(12345);
    for (const cfg of layerConfigs) {
        const stars = [];
        // Tile size for wrapping — large enough to cover any screen
        const tileSize = 4000;
        for (let i = 0; i < cfg.count; i++) {
            stars.push({
                x: rng() * tileSize,
                y: rng() * tileSize,
                size: cfg.sizeMin + rng() * (cfg.sizeMax - cfg.sizeMin),
                alpha: cfg.alphaMin + rng() * (cfg.alphaMax - cfg.alphaMin),
                twinkleSpeed: 0.5 + rng() * 2.0,
                twinkleOffset: rng() * Math.PI * 2,
            });
        }
        starLayers.push({ stars, parallax: cfg.parallax, tileSize });
    }
}

function drawStarfield(now) {
    for (const layer of starLayers) {
        const offX = (cameraX * layer.parallax) % layer.tileSize;
        const offY = (cameraY * layer.parallax) % layer.tileSize;
        for (const star of layer.stars) {
            // Wrap star position relative to camera
            let sx = ((star.x - offX) % layer.tileSize + layer.tileSize) % layer.tileSize;
            let sy = ((star.y - offY) % layer.tileSize + layer.tileSize) % layer.tileSize;
            // Tile to cover screen
            if (sx > canvas.width + 2) continue;
            if (sy > canvas.height + 2) continue;
            const twinkle = 0.7 + 0.3 * Math.sin(now * 0.001 * star.twinkleSpeed + star.twinkleOffset);
            const a = star.alpha * twinkle;
            ctx.fillStyle = `rgba(255, 255, 255, ${a})`;
            ctx.fillRect(sx, sy, star.size, star.size);
        }
    }
}

function render() {
    requestAnimationFrame(render);

    const now = performance.now();

    frameCount++;
    if (now - lastFpsTime >= 1000) {
        fps = frameCount;
        frameCount = 0;
        lastFpsTime = now;
    }

    let t = 0;
    if (lastTickTime > 0) {
        t = (now - lastTickTime) / TICK_INTERVAL;
        t = Math.max(0, Math.min(t, 2.0));
    }

    interpolateEntities(t);

    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    const myEntity = entities.get(myEntityId);
    if (myEntity) {
        cameraX = myEntity.renderX - canvas.width / 2;
        cameraY = myEntity.renderY - canvas.height / 2;
    }

    // Draw starfield
    drawStarfield(now);

    // Draw grid
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.04)';
    ctx.lineWidth = 1;
    const gridSize = 200;
    const startX = Math.floor(cameraX / gridSize) * gridSize;
    const startY = Math.floor(cameraY / gridSize) * gridSize;
    for (let x = startX; x < cameraX + canvas.width; x += gridSize) {
        ctx.beginPath();
        ctx.moveTo(x - cameraX, 0);
        ctx.lineTo(x - cameraX, canvas.height);
        ctx.stroke();
    }
    for (let y = startY; y < cameraY + canvas.height; y += gridSize) {
        ctx.beginPath();
        ctx.moveTo(0, y - cameraY);
        ctx.lineTo(canvas.width, y - cameraY);
        ctx.stroke();
    }

    // Draw entities
    for (const [id, ent] of entities) {
        const e = ent.curr;
        const sx = ent.renderX - cameraX;
        const sy = ent.renderY - cameraY;

        if (sx < -100 || sx > canvas.width + 100 || sy < -100 || sy > canvas.height + 100) continue;

        let color = ENTITY_COLORS[e.entityType] || '#fff';
        if (e.entityType === EntityType.ASTEROID) {
            color = RESOURCE_COLORS[e.resourceType] || '#a86';
        }

        switch (e.entityType) {
            case EntityType.SHIP: {
                // Determine if ship is thrusting
                let thrusting = false;
                if (id === myEntityId) {
                    thrusting = !!(keys['KeyW'] || keys['ArrowUp']);
                } else {
                    const spd = Math.sqrt(e.vx * e.vx + e.vy * e.vy);
                    thrusting = spd > 30;
                }
                updateThrusterParticles(ent, thrusting, 1 / 60);
                drawThrusterParticles(ent, id === myEntityId);
                drawShip(sx, sy, ent.renderRot, e.width, e.height, color, id === myEntityId, e.health, e.shield, e.pilotName, thrusting);
                break;
            }
            case EntityType.ASTEROID:
                drawMinable(sx, sy, e.radius, e.resourceType, e.resourceRemaining, color, ent.renderRot);
                break;
            case EntityType.PROJECTILE:
                drawProjectile(sx, sy, ent.renderRot, color);
                break;
            case EntityType.STATION:
                drawStation(sx, sy, e.radius || 80);
                break;
            case EntityType.LOOT_CRATE:
                drawLootCrate(sx, sy, e.radius || 12);
                break;
            default:
                ctx.beginPath();
                ctx.arc(sx, sy, e.radius || 5, 0, Math.PI * 2);
                ctx.fillStyle = color;
                ctx.fill();
        }
    }

    // Draw target highlight + info
    if (targetId && entities.has(targetId)) {
        const tgt = entities.get(targetId);
        const tx = tgt.renderX - cameraX;
        const ty = tgt.renderY - cameraY;
        const tr = (tgt.curr.radius || 20) + 8;

        ctx.save();

        if (tgt.curr.entityType === EntityType.LOOT_CRATE) {
            // Loot crate target
            ctx.strokeStyle = '#fd0';
            ctx.lineWidth = 2;
            ctx.setLineDash([6, 4]);
            ctx.beginPath();
            ctx.arc(tx, ty, tr, 0, Math.PI * 2);
            ctx.stroke();
            ctx.setLineDash([]);

            ctx.font = '12px monospace';
            ctx.textAlign = 'center';
            ctx.fillStyle = '#fd0';
            ctx.fillText('LOOT CRATE', tx, ty - tr - 14);
            const res = tgt.curr.resources;
            if (res && res.length >= 4) {
                let infoY = ty - tr - 2;
                for (let i = 0; i < 4; i++) {
                    if (res[i] > 0) {
                        ctx.fillStyle = RESOURCE_COLORS[i];
                        ctx.fillText(`${RESOURCE_NAMES[i]}: ${Math.floor(res[i])}`, tx, infoY);
                        infoY += 14;
                    }
                }
            }
            ctx.textAlign = 'left';
        } else {
            // Asteroid target
            const resType = tgt.curr.resourceType || 0;
            const resColor = RESOURCE_COLORS[resType] || '#a86';

            ctx.strokeStyle = resColor;
            ctx.lineWidth = 2;
            ctx.setLineDash([6, 4]);
            ctx.beginPath();
            ctx.arc(tx, ty, tr, 0, Math.PI * 2);
            ctx.stroke();
            ctx.setLineDash([]);

            const resName = RESOURCE_NAMES[resType] || '???';
            const remaining = Math.floor(tgt.curr.resourceRemaining || 0);
            ctx.font = '12px monospace';
            ctx.textAlign = 'center';
            ctx.fillStyle = resColor;
            ctx.fillText(resName, tx, ty - tr - 14);
            ctx.fillStyle = '#ccc';
            ctx.fillText(`${remaining} remaining`, tx, ty - tr - 2);
            ctx.textAlign = 'left';
        }

        ctx.restore();
    }

    // Draw mining lasers
    for (const [id, ent] of entities) {
        const e = ent.curr;
        if (e.miningActive && e.miningTargetId && entities.has(e.miningTargetId)) {
            const tgt = entities.get(e.miningTargetId);
            const sx = ent.renderX - cameraX;
            const sy = ent.renderY - cameraY;
            const tx = tgt.renderX - cameraX;
            const ty = tgt.renderY - cameraY;

            // Pulsing laser effect
            const pulse = 0.5 + 0.5 * Math.sin(performance.now() * 0.01);
            ctx.save();
            ctx.strokeStyle = `rgba(0, 255, 128, ${0.4 + pulse * 0.4})`;
            ctx.lineWidth = 2 + pulse;
            ctx.shadowColor = 'rgba(0, 255, 128, 0.6)';
            ctx.shadowBlur = 8;
            ctx.beginPath();
            ctx.moveTo(sx, sy);
            ctx.lineTo(tx, ty);
            ctx.stroke();
            ctx.shadowBlur = 0;
            ctx.restore();
        }
    }

    // Draw explosions
    updateAndDrawExplosions(now);

    // Draw death screen
    if (isDead) {
        ctx.fillStyle = 'rgba(0, 0, 0, 0.6)';
        ctx.fillRect(0, 0, canvas.width, canvas.height);

        ctx.fillStyle = '#f44';
        ctx.font = 'bold 48px monospace';
        ctx.textAlign = 'center';
        ctx.fillText('YOU DIED', canvas.width / 2, canvas.height / 2 - 30);

        ctx.fillStyle = '#aaa';
        ctx.font = '20px monospace';
        ctx.fillText('Press SPACE to respawn', canvas.width / 2, canvas.height / 2 + 20);
        ctx.textAlign = 'left';
    }

    // Track FLUX from own entity state
    if (myEntity && myEntity.curr.flux !== undefined) {
        playerFlux = myEntity.curr.flux;
    }

    // Draw HUD
    let hudText = `${playerUsername} | FPS: ${fps} | Tick: ${tickCount} | Entities: ${entities.size}`;
    if (myEntity) {
        hudText += ` | FLUX: ${Math.floor(playerFlux)}`;
        hudText += `\nPos: (${myEntity.renderX.toFixed(0)}, ${myEntity.renderY.toFixed(0)})`;
    }
    hud.textContent = hudText;

    // Draw player status bars at bottom center
    if (myEntity) {
        const barW = 200;
        const barH = 14;
        const bx = canvas.width / 2 - barW / 2;
        const by = canvas.height - 60;
        const hp = myEntity.curr.health;
        const sh = myEntity.curr.shield;

        // Shield bar
        ctx.fillStyle = 'rgba(80,130,255,0.2)';
        ctx.fillRect(bx, by, barW, barH);
        ctx.fillStyle = 'rgba(80,130,255,0.8)';
        ctx.fillRect(bx, by, barW * sh, barH);
        ctx.strokeStyle = 'rgba(80,130,255,0.5)';
        ctx.lineWidth = 1;
        ctx.strokeRect(bx, by, barW, barH);
        ctx.fillStyle = '#fff';
        ctx.font = '11px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText(`SHIELD ${(sh * 100).toFixed(0)}%`, bx + barW / 2, by + barH / 2);

        // Health bar
        const hy = by + barH + 4;
        ctx.fillStyle = 'rgba(255,60,60,0.2)';
        ctx.fillRect(bx, hy, barW, barH);
        ctx.fillStyle = hp > 0.3 ? 'rgba(255,60,60,0.8)' : 'rgba(255,30,30,1)';
        ctx.fillRect(bx, hy, barW * hp, barH);
        ctx.strokeStyle = 'rgba(255,60,60,0.5)';
        ctx.lineWidth = 1;
        ctx.strokeRect(bx, hy, barW, barH);
        ctx.fillStyle = '#fff';
        ctx.fillText(`HP ${(hp * 100).toFixed(0)}%`, bx + barW / 2, hy + barH / 2);
        ctx.textAlign = 'left';
        ctx.textBaseline = 'alphabetic';

        // Cargo bar
        const res = myEntity.curr.resources;
        if (res && res.length >= 4) {
            const totalCargo = res.reduce((a, b) => a + b, 0);
            const cargoFrac = Math.min(totalCargo / MAX_CARGO, 1);
            const cargoFull = cargoFrac >= 1;

            const cy2 = hy + barH + 4;
            ctx.fillStyle = 'rgba(200,150,50,0.2)';
            ctx.fillRect(bx, cy2, barW, barH);
            const cargoColor = cargoFull
                ? `rgba(255,60,60,${0.7 + 0.3 * Math.sin(performance.now() * 0.006)})`
                : 'rgba(200,150,50,0.8)';
            ctx.fillStyle = cargoColor;
            ctx.fillRect(bx, cy2, barW * cargoFrac, barH);
            ctx.strokeStyle = cargoFull ? 'rgba(255,60,60,0.7)' : 'rgba(200,150,50,0.5)';
            ctx.lineWidth = 1;
            ctx.strokeRect(bx, cy2, barW, barH);
            ctx.fillStyle = '#fff';
            ctx.font = '11px monospace';
            ctx.textAlign = 'center';
            ctx.textBaseline = 'middle';
            const cargoLabel = cargoFull
                ? 'CARGO FULL'
                : `CARGO ${Math.floor(totalCargo)}/${MAX_CARGO}`;
            ctx.fillText(cargoLabel, bx + barW / 2, cy2 + barH / 2);
            ctx.textAlign = 'left';
            ctx.textBaseline = 'alphabetic';
        }
    }

    // Draw cargo panel
    if (cargoPanelOpen && myEntity) {
        drawCargoPanel(myEntity);
    }

    // Station proximity prompt
    if (myEntity && !isDead) {
        for (const [id, ent] of entities) {
            if (ent.curr.entityType !== EntityType.STATION) continue;
            const dx = myEntity.renderX - ent.renderX;
            const dy = myEntity.renderY - ent.renderY;
            const dist = Math.sqrt(dx * dx + dy * dy);
            if (dist < 250) {
                ctx.save();
                ctx.font = 'bold 14px monospace';
                ctx.textAlign = 'center';
                ctx.fillStyle = '#8f8';
                const barY = canvas.height - 60;
                ctx.fillText('Press E to sell cargo', canvas.width / 2, barY - 20);
                ctx.restore();
                break;
            }
        }
    }

    // Draw toasts
    const toastNow = performance.now();
    const TOAST_DURATION = 3000;
    toasts = toasts.filter(t => toastNow - t.time < TOAST_DURATION);
    for (let i = 0; i < toasts.length; i++) {
        const t = toasts[i];
        const age = toastNow - t.time;
        let alpha = 1;
        if (age > 2000) alpha = 1 - (age - 2000) / 1000;
        ctx.save();
        ctx.font = 'bold 18px monospace';
        ctx.textAlign = 'center';
        ctx.fillStyle = `rgba(100, 255, 100, ${alpha})`;
        ctx.fillText(t.text, canvas.width / 2, canvas.height / 2 - 80 - i * 24);
        ctx.restore();
    }
}

function drawCargoPanel(myEntity) {
    const res = myEntity.curr.resources;
    if (!res || res.length < 4) return;

    const pw = 260;
    const rowH = 32;
    const ph = 30 + 4 * rowH + 40; // title + rows + footer
    const px = canvas.width / 2 - pw / 2;
    const py = canvas.height / 2 - ph / 2;

    // Panel background
    ctx.fillStyle = 'rgba(10, 12, 20, 0.92)';
    ctx.fillRect(px, py, pw, ph);
    ctx.strokeStyle = 'rgba(200, 150, 50, 0.6)';
    ctx.lineWidth = 1;
    ctx.strokeRect(px, py, pw, ph);

    // Title
    ctx.fillStyle = '#dda';
    ctx.font = 'bold 14px monospace';
    ctx.textAlign = 'center';
    ctx.fillText('CARGO HOLD', px + pw / 2, py + 22);

    const totalCargo = res.reduce((a, b) => a + b, 0);

    // Resource rows
    for (let i = 0; i < 4; i++) {
        const ry = py + 30 + i * rowH;
        const amount = res[i];
        const frac = amount / MAX_CARGO;

        // Bar background
        ctx.fillStyle = 'rgba(255,255,255,0.05)';
        ctx.fillRect(px + 10, ry + 4, pw - 20, rowH - 8);

        // Bar fill
        ctx.fillStyle = RESOURCE_COLORS[i];
        ctx.globalAlpha = 0.3;
        ctx.fillRect(px + 10, ry + 4, (pw - 20) * frac, rowH - 8);
        ctx.globalAlpha = 1;

        // Label
        ctx.fillStyle = RESOURCE_COLORS[i];
        ctx.font = '13px monospace';
        ctx.textAlign = 'left';
        ctx.fillText(`[${i + 1}] ${RESOURCE_NAMES[i]}`, px + 16, ry + 20);

        // Amount
        ctx.textAlign = 'right';
        ctx.fillStyle = '#eee';
        ctx.fillText(Math.floor(amount).toString(), px + pw - 16, ry + 20);
    }

    // Footer
    const fy = py + 30 + 4 * rowH + 8;
    ctx.textAlign = 'center';
    ctx.font = '11px monospace';
    ctx.fillStyle = totalCargo >= MAX_CARGO
        ? `rgba(255,80,80,${0.7 + 0.3 * Math.sin(performance.now() * 0.006)})`
        : '#888';
    ctx.fillText(`${Math.floor(totalCargo)} / ${MAX_CARGO}  |  Press 1-4 to jettison`, px + pw / 2, fy + 10);
    ctx.textAlign = 'left';
}

function drawShip(x, y, rotation, w, h, color, isMe, health, shield, pilotName, thrusting) {
    const hw = (w || 60) / 2;
    const hh = (h || 30) / 2;
    const now = performance.now();
    const baseColor = isMe ? '#0f0' : color;
    const glowColor = isMe ? 'rgba(0,255,0,' : 'rgba(100,170,255,';
    const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);

    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rotation);

    // === Ion thruster exhaust (only when thrusting) ===
    if (thrusting) {
        const flicker = 0.7 + 0.3 * Math.sin(now * 0.015 + Math.random() * 0.5);

        // Outer exhaust plume — faint blue
        ctx.beginPath();
        ctx.moveTo(-hw * 0.7, -hh * 0.55);
        ctx.lineTo(-hw * (1.3 + flicker * 0.3), 0);
        ctx.lineTo(-hw * 0.7, hh * 0.55);
        ctx.closePath();
        ctx.fillStyle = `rgba(60, 120, 255, 0.1)`;
        ctx.fill();

        // Main exhaust glow — bright ion blue
        ctx.beginPath();
        ctx.moveTo(-hw * 0.7, -hh * 0.4);
        ctx.lineTo(-hw * (1.1 + flicker * 0.25), 0);
        ctx.lineTo(-hw * 0.7, hh * 0.4);
        ctx.closePath();
        ctx.fillStyle = `rgba(80, 150, 255, ${0.2 + flicker * 0.25})`;
        ctx.shadowColor = 'rgba(80, 150, 255, 0.7)';
        ctx.shadowBlur = 10;
        ctx.fill();
        ctx.shadowBlur = 0;

        // Hot core — white/cyan
        ctx.beginPath();
        ctx.moveTo(-hw * 0.72, -hh * 0.12);
        ctx.lineTo(-hw * (0.9 + flicker * 0.15), 0);
        ctx.lineTo(-hw * 0.72, hh * 0.12);
        ctx.closePath();
        ctx.fillStyle = `rgba(200, 230, 255, ${0.3 + flicker * 0.4})`;
        ctx.fill();
    }

    // === Main hull ===

    // Hull outline path (reused for fill and stroke)
    function hullPath() {
        ctx.beginPath();
        ctx.moveTo(hw, 0);                    // nose tip
        ctx.lineTo(hw * 0.55, -hh * 0.42);   // upper nose bevel
        ctx.lineTo(hw * 0.1, -hh * 0.52);    // upper mid
        ctx.lineTo(-hw * 0.1, -hh * 0.6);    // wing root top
        ctx.lineTo(-hw * 0.25, -hh * 1.25);  // wing tip top
        ctx.lineTo(-hw * 0.55, -hh * 1.1);   // wing back top
        ctx.lineTo(-hw * 0.7, -hh * 0.5);    // rear top
        ctx.lineTo(-hw * 0.7, hh * 0.5);     // rear bottom
        ctx.lineTo(-hw * 0.55, hh * 1.1);    // wing back bottom
        ctx.lineTo(-hw * 0.25, hh * 1.25);   // wing tip bottom
        ctx.lineTo(-hw * 0.1, hh * 0.6);     // wing root bottom
        ctx.lineTo(hw * 0.1, hh * 0.52);     // lower mid
        ctx.lineTo(hw * 0.55, hh * 0.42);    // lower nose bevel
        ctx.closePath();
    }

    // Hull fill
    hullPath();
    ctx.fillStyle = baseColor;
    ctx.globalAlpha = 0.1;
    ctx.fill();
    ctx.globalAlpha = 1;

    // Hull stroke
    hullPath();
    ctx.strokeStyle = baseColor;
    ctx.lineWidth = 2;
    ctx.stroke();

    // === Hull panel lines ===
    ctx.strokeStyle = baseColor;
    ctx.lineWidth = 1;
    ctx.globalAlpha = 0.2;

    // Center spine
    ctx.beginPath();
    ctx.moveTo(hw * 0.6, 0);
    ctx.lineTo(-hw * 0.65, 0);
    ctx.stroke();

    // Forward cross-brace
    ctx.beginPath();
    ctx.moveTo(hw * 0.3, -hh * 0.35);
    ctx.lineTo(hw * 0.3, hh * 0.35);
    ctx.stroke();

    // Mid cross-brace
    ctx.beginPath();
    ctx.moveTo(-hw * 0.1, -hh * 0.55);
    ctx.lineTo(-hw * 0.1, hh * 0.55);
    ctx.stroke();

    // Aft cross-brace
    ctx.beginPath();
    ctx.moveTo(-hw * 0.5, -hh * 0.7);
    ctx.lineTo(-hw * 0.5, hh * 0.7);
    ctx.stroke();

    ctx.globalAlpha = 1;

    // === Wing detail ===
    ctx.globalAlpha = 0.3;
    ctx.strokeStyle = baseColor;
    ctx.lineWidth = 1;

    // Top wing spar
    ctx.beginPath();
    ctx.moveTo(hw * 0.0, -hh * 0.55);
    ctx.lineTo(-hw * 0.45, -hh * 1.05);
    ctx.stroke();

    // Top wing rib
    ctx.beginPath();
    ctx.moveTo(-hw * 0.18, -hh * 0.9);
    ctx.lineTo(-hw * 0.38, -hh * 0.7);
    ctx.stroke();

    // Bottom wing spar
    ctx.beginPath();
    ctx.moveTo(hw * 0.0, hh * 0.55);
    ctx.lineTo(-hw * 0.45, hh * 1.05);
    ctx.stroke();

    // Bottom wing rib
    ctx.beginPath();
    ctx.moveTo(-hw * 0.18, hh * 0.9);
    ctx.lineTo(-hw * 0.38, hh * 0.7);
    ctx.stroke();

    // Wing tip accents
    ctx.fillStyle = baseColor;
    ctx.globalAlpha = 0.15;
    // Top wing panel fill
    ctx.beginPath();
    ctx.moveTo(-hw * 0.1, -hh * 0.6);
    ctx.lineTo(-hw * 0.25, -hh * 1.25);
    ctx.lineTo(-hw * 0.55, -hh * 1.1);
    ctx.lineTo(-hw * 0.35, -hh * 0.6);
    ctx.closePath();
    ctx.fill();
    // Bottom wing panel fill
    ctx.beginPath();
    ctx.moveTo(-hw * 0.1, hh * 0.6);
    ctx.lineTo(-hw * 0.25, hh * 1.25);
    ctx.lineTo(-hw * 0.55, hh * 1.1);
    ctx.lineTo(-hw * 0.35, hh * 0.6);
    ctx.closePath();
    ctx.fill();

    ctx.globalAlpha = 1;

    // === Cockpit canopy ===
    ctx.beginPath();
    ctx.moveTo(hw * 0.8, 0);
    ctx.lineTo(hw * 0.35, -hh * 0.22);
    ctx.lineTo(hw * 0.15, -hh * 0.18);
    ctx.lineTo(hw * 0.15, hh * 0.18);
    ctx.lineTo(hw * 0.35, hh * 0.22);
    ctx.closePath();
    ctx.fillStyle = baseColor;
    ctx.globalAlpha = 0.35;
    ctx.fill();
    ctx.globalAlpha = 1;
    ctx.strokeStyle = baseColor;
    ctx.lineWidth = 1;
    ctx.globalAlpha = 0.5;
    ctx.stroke();
    ctx.globalAlpha = 1;

    // Cockpit frame line
    ctx.beginPath();
    ctx.moveTo(hw * 0.5, -hh * 0.12);
    ctx.lineTo(hw * 0.5, hh * 0.12);
    ctx.strokeStyle = baseColor;
    ctx.globalAlpha = 0.3;
    ctx.lineWidth = 1;
    ctx.stroke();
    ctx.globalAlpha = 1;

    // === Engine section ===

    // Engine housing top
    ctx.fillStyle = baseColor;
    ctx.globalAlpha = 0.15;
    ctx.fillRect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = baseColor;
    ctx.globalAlpha = 0.4;
    ctx.lineWidth = 1;
    ctx.strokeRect(-hw * 0.72, -hh * 0.48, hw * 0.12, hh * 0.35);
    ctx.globalAlpha = 1;

    // Engine housing bottom
    ctx.fillStyle = baseColor;
    ctx.globalAlpha = 0.15;
    ctx.fillRect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = baseColor;
    ctx.globalAlpha = 0.4;
    ctx.lineWidth = 1;
    ctx.strokeRect(-hw * 0.72, hh * 0.13, hw * 0.12, hh * 0.35);
    ctx.globalAlpha = 1;

    // Engine nozzle glow (ion blue, bright when thrusting)
    const nozzleAlpha = thrusting ? (0.6 + pulse * 0.4) : 0.15;
    ctx.fillStyle = `rgba(80, 150, 255, ${nozzleAlpha})`;
    ctx.fillRect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22);
    ctx.fillRect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22);
    if (thrusting) {
        ctx.shadowColor = 'rgba(80, 150, 255, 0.6)';
        ctx.shadowBlur = 5;
        ctx.fillRect(-hw * 0.73, -hh * 0.38, hw * 0.04, hh * 0.22);
        ctx.fillRect(-hw * 0.73, hh * 0.16, hw * 0.04, hh * 0.22);
        ctx.shadowBlur = 0;
    }

    // === Nav lights (steady white) ===
    const navAlpha = 0.5 + pulse * 0.3;

    // Wing tip lights
    for (const wy of [-hh * 1.23, hh * 1.23]) {
        ctx.beginPath();
        ctx.arc(-hw * 0.25, wy, 1.5, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha})`;
        ctx.shadowColor = 'rgba(255, 255, 255, 0.6)';
        ctx.shadowBlur = 5;
        ctx.fill();
        ctx.shadowBlur = 0;
    }

    // Nose light
    ctx.beginPath();
    ctx.arc(hw * 0.95, 0, 1, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha})`;
    ctx.shadowColor = 'rgba(255, 255, 255, 0.6)';
    ctx.shadowBlur = 4;
    ctx.fill();
    ctx.shadowBlur = 0;

    // Tail light
    ctx.beginPath();
    ctx.arc(-hw * 0.68, 0, 1.5, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(255, 255, 255, ${navAlpha * 0.7})`;
    ctx.shadowColor = 'rgba(255, 255, 255, 0.4)';
    ctx.shadowBlur = 4;
    ctx.fill();
    ctx.shadowBlur = 0;

    // === Hull accent — nose stripe ===
    ctx.beginPath();
    ctx.moveTo(hw * 0.85, 0);
    ctx.lineTo(hw * 0.55, -hh * 0.4);
    ctx.strokeStyle = baseColor;
    ctx.globalAlpha = 0.15;
    ctx.lineWidth = 2;
    ctx.stroke();
    ctx.beginPath();
    ctx.moveTo(hw * 0.85, 0);
    ctx.lineTo(hw * 0.55, hh * 0.4);
    ctx.stroke();
    ctx.globalAlpha = 1;

    ctx.restore();

    // === Screen-space indicators (unrotated) ===
    const barW = Math.max(hw * 2, 40);
    const barH = 3;
    const barGap = 2;
    const barX = x - barW / 2;
    const shipTop = y - Math.sqrt(hw * hw + hh * hh) - 8;

    // HP bar
    const hpY = shipTop - barH;
    ctx.fillStyle = 'rgba(255,60,60,0.15)';
    ctx.fillRect(barX, hpY, barW, barH);
    const hpColor = health > 0.3 ? 'rgba(255,60,60,0.9)' : 'rgba(255,30,30,1)';
    ctx.fillStyle = hpColor;
    ctx.fillRect(barX, hpY, barW * health, barH);
    if (health <= 0.3) {
        ctx.shadowColor = 'rgba(255,30,30,0.6)';
        ctx.shadowBlur = 6;
        ctx.fillRect(barX, hpY, barW * health, barH);
        ctx.shadowBlur = 0;
    }

    // Shield bar
    const shY = hpY - barH - barGap;
    ctx.fillStyle = 'rgba(80,130,255,0.15)';
    ctx.fillRect(barX, shY, barW, barH);
    if (shield > 0) {
        ctx.fillStyle = 'rgba(80,130,255,0.9)';
        ctx.shadowColor = 'rgba(80,130,255,0.5)';
        ctx.shadowBlur = 4;
        ctx.fillRect(barX, shY, barW * shield, barH);
        ctx.shadowBlur = 0;
    }

    // Pilot name
    if (pilotName) {
        const nameY = shY - 4;
        ctx.font = '10px monospace';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'bottom';
        ctx.fillStyle = isMe ? 'rgba(0,255,0,0.8)' : 'rgba(200,220,255,0.7)';
        ctx.fillText(pilotName, x, nameY);
    }
}

function drawMinable(x, y, radius, resourceType, remaining, color, rotation) {
    switch (resourceType) {
        case ResourceType.ORE: drawOreAsteroid(x, y, radius, rotation); break;
        case ResourceType.CRYSTAL: drawCrystalAsteroid(x, y, radius, rotation); break;
        case ResourceType.GAS: drawGasCloud(x, y, radius); break;
        case ResourceType.METAL: drawMetalAsteroid(x, y, radius, rotation); break;
        default: drawOreAsteroid(x, y, radius, rotation); break;
    }
}

// Ore — jagged rocky body with metallic veins
function drawOreAsteroid(x, y, radius, rotation) {
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rotation);

    // Rocky body — irregular polygon
    const sides = 10;
    ctx.beginPath();
    for (let i = 0; i < sides; i++) {
        const angle = (i / sides) * Math.PI * 2;
        const jag = 0.72 + Math.sin(i * 5.1 + 2.3) * 0.18 + Math.cos(i * 3.7) * 0.1;
        const r = radius * jag;
        const px = Math.cos(angle) * r;
        const py = Math.sin(angle) * r;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
    }
    ctx.closePath();
    ctx.fillStyle = '#3a2a15';
    ctx.fill();
    ctx.strokeStyle = '#c90';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Surface cracks / veins
    for (let i = 0; i < 3; i++) {
        const a1 = (i * 2.2 + 0.5);
        const a2 = a1 + 0.6 + Math.sin(i * 4.1) * 0.3;
        ctx.beginPath();
        ctx.moveTo(Math.cos(a1) * radius * 0.2, Math.sin(a1) * radius * 0.2);
        ctx.lineTo(Math.cos(a2) * radius * 0.6, Math.sin(a2) * radius * 0.6);
        ctx.strokeStyle = 'rgba(255, 180, 40, 0.5)';
        ctx.lineWidth = 1.5;
        ctx.stroke();
    }

    // Ore glint highlights
    for (let i = 0; i < 4; i++) {
        const ga = i * 1.7 + 0.8;
        const gr = radius * (0.25 + Math.sin(i * 3.3) * 0.2);
        const gx = Math.cos(ga) * gr;
        const gy = Math.sin(ga) * gr;
        ctx.beginPath();
        ctx.arc(gx, gy, radius * 0.06, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(255, 200, 60, 0.7)';
        ctx.fill();
    }

    ctx.restore();
}

// Crystal — sharp faceted spikes radiating from center
function drawCrystalAsteroid(x, y, radius, rotation) {
    const now = performance.now();
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rotation);

    // Core glow
    const glow = ctx.createRadialGradient(0, 0, 0, 0, 0, radius * 0.6);
    glow.addColorStop(0, 'rgba(170, 68, 255, 0.25)');
    glow.addColorStop(1, 'rgba(170, 68, 255, 0)');
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(0, 0, radius * 0.6, 0, Math.PI * 2);
    ctx.fill();

    // Crystal spikes
    const spikes = 6;
    for (let i = 0; i < spikes; i++) {
        const angle = (i / spikes) * Math.PI * 2;
        const len = radius * (0.7 + Math.sin(i * 2.9 + 1.1) * 0.3);
        const width = radius * (0.12 + Math.sin(i * 4.3) * 0.05);
        const shimmer = 0.5 + 0.3 * Math.sin(now * 0.003 + i * 1.5);

        ctx.save();
        ctx.rotate(angle);

        // Spike shape — elongated diamond
        ctx.beginPath();
        ctx.moveTo(len, 0);
        ctx.lineTo(len * 0.3, -width);
        ctx.lineTo(0, 0);
        ctx.lineTo(len * 0.3, width);
        ctx.closePath();

        ctx.fillStyle = `rgba(180, 100, 255, ${0.25 + shimmer * 0.2})`;
        ctx.fill();
        ctx.strokeStyle = `rgba(200, 140, 255, ${0.6 + shimmer * 0.3})`;
        ctx.lineWidth = 1;
        ctx.stroke();

        ctx.restore();
    }

    // Center facet
    ctx.beginPath();
    for (let i = 0; i < spikes; i++) {
        const angle = (i / spikes) * Math.PI * 2;
        const r = radius * 0.22;
        const px = Math.cos(angle) * r;
        const py = Math.sin(angle) * r;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
    }
    ctx.closePath();
    ctx.fillStyle = 'rgba(200, 150, 255, 0.4)';
    ctx.fill();
    ctx.strokeStyle = 'rgba(220, 180, 255, 0.7)';
    ctx.lineWidth = 1;
    ctx.stroke();

    ctx.restore();
}

// Gas — soft nebulous cloud with animated wisps
function drawGasCloud(x, y, radius) {
    const now = performance.now();
    ctx.save();

    // Multiple layered translucent blobs
    const blobs = 5;
    for (let i = 0; i < blobs; i++) {
        const phase = now * 0.0008 + i * 1.3;
        const drift = Math.sin(phase) * radius * 0.15;
        const driftY = Math.cos(phase * 0.7 + i) * radius * 0.12;
        const blobR = radius * (0.5 + Math.sin(i * 2.1 + 0.5) * 0.25);
        const bx = x + Math.cos(i * 1.26) * radius * 0.3 + drift;
        const by = y + Math.sin(i * 1.26) * radius * 0.3 + driftY;

        const grad = ctx.createRadialGradient(bx, by, 0, bx, by, blobR);
        grad.addColorStop(0, `rgba(60, 200, 240, ${0.15 + Math.sin(phase) * 0.05})`);
        grad.addColorStop(0.5, `rgba(40, 160, 220, ${0.08})`);
        grad.addColorStop(1, 'rgba(40, 160, 220, 0)');
        ctx.fillStyle = grad;
        ctx.beginPath();
        ctx.arc(bx, by, blobR, 0, Math.PI * 2);
        ctx.fill();
    }

    // Bright core
    const coreGrad = ctx.createRadialGradient(x, y, 0, x, y, radius * 0.35);
    coreGrad.addColorStop(0, 'rgba(100, 230, 255, 0.3)');
    coreGrad.addColorStop(1, 'rgba(60, 200, 240, 0)');
    ctx.fillStyle = coreGrad;
    ctx.beginPath();
    ctx.arc(x, y, radius * 0.35, 0, Math.PI * 2);
    ctx.fill();

    // Wisp particles
    for (let i = 0; i < 6; i++) {
        const wPhase = now * 0.001 + i * 1.05;
        const wa = (i / 6) * Math.PI * 2 + Math.sin(wPhase) * 0.5;
        const wr = radius * (0.3 + Math.sin(wPhase * 0.8) * 0.3);
        const wx = x + Math.cos(wa) * wr;
        const wy = y + Math.sin(wa) * wr;
        const walpha = 0.3 + 0.2 * Math.sin(wPhase * 1.3);
        ctx.beginPath();
        ctx.arc(wx, wy, radius * 0.04, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(150, 240, 255, ${walpha})`;
        ctx.fill();
    }

    // Outer boundary ring (subtle)
    ctx.beginPath();
    ctx.arc(x, y, radius, 0, Math.PI * 2);
    ctx.strokeStyle = 'rgba(60, 200, 240, 0.12)';
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 6]);
    ctx.stroke();
    ctx.setLineDash([]);

    ctx.restore();
}

// Metal — smooth faceted geometric shape with reflective panels
function drawMetalAsteroid(x, y, radius, rotation) {
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rotation);

    // Main body — smooth hexagonal-ish shape
    const sides = 7;
    const points = [];
    for (let i = 0; i < sides; i++) {
        const angle = (i / sides) * Math.PI * 2;
        const r = radius * (0.85 + Math.sin(i * 2.6 + 1.0) * 0.12);
        points.push({ x: Math.cos(angle) * r, y: Math.sin(angle) * r });
    }

    ctx.beginPath();
    for (let i = 0; i < points.length; i++) {
        if (i === 0) ctx.moveTo(points[i].x, points[i].y);
        else ctx.lineTo(points[i].x, points[i].y);
    }
    ctx.closePath();
    ctx.fillStyle = '#2a2a2e';
    ctx.fill();
    ctx.strokeStyle = '#999';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Facet lines from center to each vertex — panel divisions
    for (let i = 0; i < points.length; i++) {
        ctx.beginPath();
        ctx.moveTo(0, 0);
        ctx.lineTo(points[i].x * 0.95, points[i].y * 0.95);
        ctx.strokeStyle = 'rgba(180, 180, 190, 0.2)';
        ctx.lineWidth = 1;
        ctx.stroke();
    }

    // Reflective highlight panels (alternating)
    for (let i = 0; i < points.length; i++) {
        const next = (i + 1) % points.length;
        if (i % 2 === 0) {
            ctx.beginPath();
            ctx.moveTo(0, 0);
            ctx.lineTo(points[i].x, points[i].y);
            ctx.lineTo(points[next].x, points[next].y);
            ctx.closePath();
            ctx.fillStyle = 'rgba(200, 200, 210, 0.08)';
            ctx.fill();
        }
    }

    // Specular highlight
    ctx.beginPath();
    ctx.arc(-radius * 0.2, -radius * 0.2, radius * 0.15, 0, Math.PI * 2);
    ctx.fillStyle = 'rgba(255, 255, 255, 0.15)';
    ctx.fill();

    // Small rivets/bolts
    for (let i = 0; i < points.length; i++) {
        ctx.beginPath();
        ctx.arc(points[i].x * 0.7, points[i].y * 0.7, radius * 0.03, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(160, 160, 170, 0.6)';
        ctx.fill();
    }

    ctx.restore();
}

function drawProjectile(x, y, rotation, color) {
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rotation);

    ctx.beginPath();
    ctx.moveTo(6, 0);
    ctx.lineTo(-3, -2);
    ctx.lineTo(-3, 2);
    ctx.closePath();
    ctx.fillStyle = color;
    ctx.fill();

    ctx.shadowColor = color;
    ctx.shadowBlur = 8;
    ctx.fill();
    ctx.shadowBlur = 0;

    ctx.restore();
}

function drawStation(x, y, radius) {
    const now = performance.now();
    const slowSpin = now * 0.0002;
    const pulse = 0.7 + 0.3 * Math.sin(now * 0.002);
    const sides = 8;
    const r = radius * 1.6; // visual size larger than collider

    ctx.save();

    // Ambient glow beneath station
    const ambientGrad = ctx.createRadialGradient(x, y, 0, x, y, r * 1.8);
    ambientGrad.addColorStop(0, `rgba(100, 255, 140, 0.06)`);
    ambientGrad.addColorStop(0.5, `rgba(80, 200, 120, 0.03)`);
    ambientGrad.addColorStop(1, 'rgba(0, 0, 0, 0)');
    ctx.fillStyle = ambientGrad;
    ctx.beginPath();
    ctx.arc(x, y, r * 1.8, 0, Math.PI * 2);
    ctx.fill();

    // Outer ring (octagon) — main hull structure, slow rotation
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(slowSpin);

    // Outer octagon fill
    ctx.beginPath();
    for (let i = 0; i < sides; i++) {
        const angle = (i / sides) * Math.PI * 2;
        const px = Math.cos(angle) * r;
        const py = Math.sin(angle) * r;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
    }
    ctx.closePath();
    ctx.fillStyle = 'rgba(100, 255, 140, 0.04)';
    ctx.fill();
    ctx.strokeStyle = 'rgba(136, 255, 136, 0.7)';
    ctx.lineWidth = 2;
    ctx.stroke();

    // Outer ring double-line detail
    ctx.beginPath();
    for (let i = 0; i < sides; i++) {
        const angle = (i / sides) * Math.PI * 2;
        const px = Math.cos(angle) * r * 0.92;
        const py = Math.sin(angle) * r * 0.92;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
    }
    ctx.closePath();
    ctx.strokeStyle = 'rgba(136, 255, 136, 0.25)';
    ctx.lineWidth = 1;
    ctx.stroke();

    // Struts from outer ring to inner hub (every other vertex)
    for (let i = 0; i < sides; i += 2) {
        const angle = (i / sides) * Math.PI * 2;
        const ox = Math.cos(angle) * r * 0.92;
        const oy = Math.sin(angle) * r * 0.92;
        const ix = Math.cos(angle) * r * 0.38;
        const iy = Math.sin(angle) * r * 0.38;
        ctx.beginPath();
        ctx.moveTo(ox, oy);
        ctx.lineTo(ix, iy);
        ctx.strokeStyle = 'rgba(136, 255, 136, 0.4)';
        ctx.lineWidth = 2;
        ctx.stroke();

        // Strut panel fill (thin parallelogram alongside strut)
        const perpAngle = angle + Math.PI / 2;
        const pw = r * 0.025;
        ctx.beginPath();
        ctx.moveTo(ox + Math.cos(perpAngle) * pw, oy + Math.sin(perpAngle) * pw);
        ctx.lineTo(ix + Math.cos(perpAngle) * pw * 0.5, iy + Math.sin(perpAngle) * pw * 0.5);
        ctx.lineTo(ix - Math.cos(perpAngle) * pw * 0.5, iy - Math.sin(perpAngle) * pw * 0.5);
        ctx.lineTo(ox - Math.cos(perpAngle) * pw, oy - Math.sin(perpAngle) * pw);
        ctx.closePath();
        ctx.fillStyle = 'rgba(136, 255, 136, 0.08)';
        ctx.fill();
    }

    // Solar panel arrays on alternating vertices
    for (let i = 1; i < sides; i += 2) {
        const angle = (i / sides) * Math.PI * 2;
        const bx = Math.cos(angle) * r;
        const by = Math.sin(angle) * r;
        const perpAngle = angle + Math.PI / 2;
        const panelLen = r * 0.35;
        const panelW = r * 0.06;

        ctx.save();
        ctx.translate(bx, by);
        ctx.rotate(angle);

        // Panel arm
        ctx.beginPath();
        ctx.moveTo(0, 0);
        ctx.lineTo(panelLen, 0);
        ctx.strokeStyle = 'rgba(136, 255, 136, 0.3)';
        ctx.lineWidth = 1;
        ctx.stroke();

        // Panel rectangles (two segments)
        for (let s = 0; s < 2; s++) {
            const px = panelLen * 0.35 + s * panelLen * 0.35;
            ctx.fillStyle = `rgba(80, 180, 120, ${0.12 + pulse * 0.06})`;
            ctx.fillRect(px - panelLen * 0.14, -panelW, panelLen * 0.28, panelW * 2);
            ctx.strokeStyle = 'rgba(136, 255, 136, 0.4)';
            ctx.lineWidth = 1;
            ctx.strokeRect(px - panelLen * 0.14, -panelW, panelLen * 0.28, panelW * 2);
            // Panel grid lines
            ctx.beginPath();
            ctx.moveTo(px, -panelW);
            ctx.lineTo(px, panelW);
            ctx.strokeStyle = 'rgba(136, 255, 136, 0.2)';
            ctx.stroke();
        }

        ctx.restore();
    }

    // Inner hub — hexagon shape
    const hubSides = 6;
    const hubR = r * 0.35;
    ctx.beginPath();
    for (let i = 0; i < hubSides; i++) {
        const angle = (i / hubSides) * Math.PI * 2;
        const px = Math.cos(angle) * hubR;
        const py = Math.sin(angle) * hubR;
        if (i === 0) ctx.moveTo(px, py);
        else ctx.lineTo(px, py);
    }
    ctx.closePath();
    ctx.fillStyle = 'rgba(100, 255, 140, 0.08)';
    ctx.fill();
    ctx.strokeStyle = 'rgba(136, 255, 136, 0.6)';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Inner detail ring
    ctx.beginPath();
    ctx.arc(0, 0, hubR * 0.55, 0, Math.PI * 2);
    ctx.strokeStyle = 'rgba(136, 255, 136, 0.3)';
    ctx.lineWidth = 1;
    ctx.stroke();

    // Center core — pulsing dot
    ctx.beginPath();
    ctx.arc(0, 0, r * 0.06, 0, Math.PI * 2);
    ctx.fillStyle = `rgba(150, 255, 180, ${0.5 + pulse * 0.5})`;
    ctx.shadowColor = 'rgba(100, 255, 140, 0.8)';
    ctx.shadowBlur = 8;
    ctx.fill();
    ctx.shadowBlur = 0;

    // Docking port markers — small rectangles on outer ring edges
    for (let i = 0; i < sides; i++) {
        const angle = (i / sides) * Math.PI * 2;
        const midAngle = ((i + 0.5) / sides) * Math.PI * 2;
        const dx = Math.cos(midAngle) * r;
        const dy = Math.sin(midAngle) * r;

        ctx.save();
        ctx.translate(dx, dy);
        ctx.rotate(midAngle);
        // Small notch
        ctx.fillStyle = `rgba(136, 255, 136, ${0.15 + pulse * 0.1})`;
        ctx.fillRect(-3, -1.5, 6, 3);
        ctx.restore();
    }

    // Blinking nav lights at 4 cardinal strut tips
    const blinkOn = Math.sin(now * 0.005) > 0;
    const blinkOn2 = Math.sin(now * 0.005 + Math.PI) > 0;
    for (let i = 0; i < sides; i += 2) {
        const angle = (i / sides) * Math.PI * 2;
        const lx = Math.cos(angle) * r * 0.98;
        const ly = Math.sin(angle) * r * 0.98;
        const on = (i % 4 === 0) ? blinkOn : blinkOn2;
        if (on) {
            ctx.beginPath();
            ctx.arc(lx, ly, 2, 0, Math.PI * 2);
            ctx.fillStyle = (i % 4 === 0) ? 'rgba(255, 80, 80, 0.9)' : 'rgba(80, 130, 255, 0.9)';
            ctx.shadowColor = ctx.fillStyle;
            ctx.shadowBlur = 6;
            ctx.fill();
            ctx.shadowBlur = 0;
        }
    }

    ctx.restore(); // undo translate+rotate

    // Label (unrotated, above station — clear of solar panels)
    const labelY = y - r * 1.4;
    ctx.font = 'bold 14px monospace';
    ctx.textAlign = 'center';
    ctx.fillStyle = `rgba(136, 255, 136, ${0.6 + pulse * 0.3})`;
    ctx.fillText('TRADE STATION', x, labelY - 14);
    ctx.font = '10px monospace';
    ctx.fillStyle = 'rgba(136, 255, 136, 0.4)';
    ctx.fillText('[ PRESS E TO SELL ]', x, labelY);
    ctx.textAlign = 'left';

    ctx.restore();
}

function drawLootCrate(x, y, radius) {
    const now = performance.now();
    const spin = now * 0.002; // slow rotation
    const pulse = 0.7 + 0.3 * Math.sin(now * 0.004);
    const r = radius * 2; // visual size larger than collider

    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(spin);

    // Diamond / rhombus shape
    ctx.beginPath();
    ctx.moveTo(0, -r);
    ctx.lineTo(r * 0.6, 0);
    ctx.lineTo(0, r);
    ctx.lineTo(-r * 0.6, 0);
    ctx.closePath();

    ctx.fillStyle = `rgba(255, 221, 0, ${0.15 * pulse})`;
    ctx.fill();
    ctx.strokeStyle = `rgba(255, 221, 0, ${0.8 * pulse})`;
    ctx.lineWidth = 2;
    ctx.shadowColor = '#fd0';
    ctx.shadowBlur = 8 * pulse;
    ctx.stroke();
    ctx.shadowBlur = 0;

    ctx.restore();
}

// === Login Screen ===

function handleLoginKeydown(e) {
    if (!loginActive) return;
    if (e.key === 'Enter' && loginInput.trim().length > 0) {
        e.preventDefault();
        playerUsername = loginInput.trim();
        spawnedOnce = false;
        localStorage.setItem('username', playerUsername);
        loggedIn = true;
        loginActive = false;
        window.removeEventListener('keydown', handleLoginKeydown);
        connect();
        requestAnimationFrame(render);
        return;
    }
    if (e.key === 'Backspace') {
        e.preventDefault();
        loginInput = loginInput.slice(0, -1);
        loginError = '';
        return;
    }
    if (e.key.length === 1 && loginInput.length < 20) {
        e.preventDefault();
        loginInput += e.key.toLowerCase();
        loginError = '';
    }
}

function renderLoginScreen() {
    if (!loginActive) return;
    requestAnimationFrame(renderLoginScreen);

    const now = performance.now();
    loginCursorTimer += 16;
    if (loginCursorTimer > 530) {
        loginCursorVisible = !loginCursorVisible;
        loginCursorTimer = 0;
    }

    ctx.fillStyle = '#000';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Starfield background
    const starSeed = 42;
    for (let i = 0; i < 120; i++) {
        const sx = ((starSeed * (i + 1) * 7919) % canvas.width);
        const sy = ((starSeed * (i + 1) * 6271) % canvas.height);
        const brightness = 0.2 + 0.4 * Math.sin(now * 0.001 + i);
        ctx.fillStyle = `rgba(255, 255, 255, ${brightness})`;
        ctx.fillRect(sx, sy, 1.5, 1.5);
    }

    const cx = canvas.width / 2;
    const cy = canvas.height / 2;

    // Panel
    const pw = 360;
    const ph = 200;
    const px = cx - pw / 2;
    const py = cy - ph / 2;

    ctx.fillStyle = 'rgba(10, 12, 20, 0.94)';
    ctx.fillRect(px, py, pw, ph);
    ctx.strokeStyle = 'rgba(68, 170, 255, 0.5)';
    ctx.lineWidth = 1;
    ctx.strokeRect(px, py, pw, ph);

    // Title
    ctx.fillStyle = '#4af';
    ctx.font = 'bold 28px monospace';
    ctx.textAlign = 'center';
    ctx.fillText('SPACE MMO', cx, py + 45);

    // Subtitle
    ctx.fillStyle = '#667';
    ctx.font = '12px monospace';
    ctx.fillText('Enter your callsign, pilot', cx, py + 68);

    // Input field
    const fieldW = pw - 60;
    const fieldH = 36;
    const fieldX = cx - fieldW / 2;
    const fieldY = py + 85;

    ctx.fillStyle = 'rgba(255, 255, 255, 0.06)';
    ctx.fillRect(fieldX, fieldY, fieldW, fieldH);
    ctx.strokeStyle = 'rgba(68, 170, 255, 0.6)';
    ctx.lineWidth = 1;
    ctx.strokeRect(fieldX, fieldY, fieldW, fieldH);

    // Input text
    ctx.fillStyle = '#eee';
    ctx.font = '16px monospace';
    ctx.textAlign = 'left';
    const displayText = loginInput + (loginCursorVisible ? '_' : '');
    ctx.fillText(displayText, fieldX + 12, fieldY + 24);

    // Error message
    ctx.textAlign = 'center';
    if (loginError) {
        ctx.fillStyle = '#f44';
        ctx.font = '13px monospace';
        ctx.fillText(loginError, cx, fieldY + fieldH + 30);
    } else if (loginInput.trim().length > 0) {
        ctx.fillStyle = '#4af';
        ctx.font = '13px monospace';
        ctx.fillText('Press ENTER to launch', cx, fieldY + fieldH + 30);
    } else {
        ctx.fillStyle = '#445';
        ctx.font = '13px monospace';
        ctx.fillText('Type a username to begin', cx, fieldY + fieldH + 30);
    }

    ctx.textAlign = 'left';
}

// Start with login screen
window.addEventListener('keydown', handleLoginKeydown);
requestAnimationFrame(renderLoginScreen);
