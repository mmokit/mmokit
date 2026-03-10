import { audio } from "../audio/audio-manager";
import type { GameState } from "../state";

let initialized = false;

export function initEscMenu(): void {
  if (initialized) return;
  initialized = true;

  const masterSlider = document.getElementById("vol-master") as HTMLInputElement;
  const musicSlider = document.getElementById("vol-music") as HTMLInputElement;
  const sfxSlider = document.getElementById("vol-sfx") as HTMLInputElement;
  const masterVal = document.getElementById("vol-master-val")!;
  const musicVal = document.getElementById("vol-music-val")!;
  const sfxVal = document.getElementById("vol-sfx-val")!;

  // Set initial slider values from AudioManager (which loads from localStorage)
  masterSlider.value = String(Math.round(audio.masterVolume * 100));
  musicSlider.value = String(Math.round(audio.musicVolume * 100));
  sfxSlider.value = String(Math.round(audio.sfxVolume * 100));
  masterVal.textContent = masterSlider.value;
  musicVal.textContent = musicSlider.value;
  sfxVal.textContent = sfxSlider.value;

  masterSlider.addEventListener("input", () => {
    const v = parseInt(masterSlider.value);
    audio.setMasterVolume(v / 100);
    masterVal.textContent = String(v);
  });

  musicSlider.addEventListener("input", () => {
    const v = parseInt(musicSlider.value);
    audio.setMusicVolume(v / 100);
    musicVal.textContent = String(v);
  });

  sfxSlider.addEventListener("input", () => {
    const v = parseInt(sfxSlider.value);
    audio.setSfxVolume(v / 100);
    sfxVal.textContent = String(v);
  });
}

export function updateEscMenu(state: GameState): void {
  const el = document.getElementById("esc-menu")!;
  el.style.display = state.escMenuOpen ? "flex" : "none";
}
