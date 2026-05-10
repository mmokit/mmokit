import "./style.css";
import App from "./app.svelte";
import { mount } from "svelte";

const target = document.getElementById("app");
if (!target) throw new Error("missing #app root");

mount(App, { target });
