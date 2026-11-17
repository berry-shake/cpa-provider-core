import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { makeT } from "../../i18n";
import { newEditor } from "../../lib/editor";
import { initialState, type State } from "../../state/store";
import { EditorDialog } from "./EditorDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// jsdom has no modal dialogs.
const dialog = HTMLDialogElement.prototype as { showModal?: () => void; close?: () => void };
if (!dialog.showModal) dialog.showModal = function (this: HTMLDialogElement) { this.setAttribute("open", ""); };
if (!dialog.close) dialog.close = function (this: HTMLDialogElement) { this.removeAttribute("open"); };

const t = makeT("en");
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("EditorDialog", () => {
  it("shows the API key as plain text the browser neither hides nor fills", () => {
    const s: State = { ...initialState(), lang: "en", view: "app", editor: newEditor(1, "add", null, "wokey") };
    act(() => root.render(<EditorDialog t={t} s={s} />));
    const key = host.querySelector<HTMLInputElement>("#f-key");
    expect(key).not.toBeNull();
    expect(key!.type).toBe("text");
    expect(key!.getAttribute("autocomplete")).toBe("off");
    expect(key!.getAttribute("autocapitalize")).toBe("off");
    expect(host.querySelector('input[type="password"]')).toBeNull();
  });

  it("clears the key and a typed proxy address once closed", () => {
    const open: State = {
      ...initialState(), lang: "en", view: "app",
      editor: { ...newEditor(1, "add", null, "wokey"), key: "sk-test-secret-key", proxyChoice: "custom", proxy: "socks5h://u:proxy-secret@127.0.0.1:9" },
    };
    act(() => root.render(<EditorDialog t={t} s={open} />));
    expect(host.querySelector<HTMLInputElement>("#f-key")!.value).toBe("sk-test-secret-key");
    expect(host.querySelector<HTMLInputElement>("#f-proxy")!.value).toBe("socks5h://u:proxy-secret@127.0.0.1:9");

    act(() => root.render(<EditorDialog t={t} s={{ ...open, editor: null }} />));
    const values = [...host.querySelectorAll<HTMLInputElement>("input")].map((input) => input.value);
    expect(values.some((value) => value.includes("secret"))).toBe(false);
    expect(host.innerHTML).not.toContain("secret");
  });
});
