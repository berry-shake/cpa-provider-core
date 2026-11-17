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
});
