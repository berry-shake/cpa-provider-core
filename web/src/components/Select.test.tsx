import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Select, type SelectOption } from "./Select";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const options: SelectOption[] = [
  { value: "", label: "Inherit" },
  { value: "direct", label: "Direct" },
  { value: "id:a", label: "att", detail: "socks5h://172.17.0.1:32041" },
  { value: "custom", label: "Custom…" },
];

let host: HTMLDivElement;
let root: Root;
let value: string;
let changes: string[];

function render() {
  act(() => {
    root.render(<Select id="sel" value={value} options={options} onChange={(next) => { changes.push(next); value = next; render(); }} />);
  });
}

const trigger = () => host.querySelector("#sel") as HTMLButtonElement;
const menu = () => host.querySelector('[role="listbox"]');
const key = (k: string) => act(() => { trigger().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  value = "id:a";
  changes = [];
  render();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

test("shows the selected label and detail and opens a listbox on click", () => {
  expect(trigger().getAttribute("role")).toBe("combobox");
  expect(trigger().textContent).toBe("attsocks5h://172.17.0.1:32041");
  expect(menu()).toBeNull();
  act(() => trigger().click());
  expect(trigger().getAttribute("aria-expanded")).toBe("true");
  const items = host.querySelectorAll('[role="option"]');
  expect(items).toHaveLength(4);
  expect(items[2].getAttribute("aria-selected")).toBe("true");
  expect(trigger().getAttribute("aria-activedescendant")).toBe(items[2].id);
});

test("clicking an option chooses it and closes the menu", () => {
  act(() => trigger().click());
  act(() => (host.querySelectorAll('[role="option"]')[1] as HTMLElement).click());
  expect(changes).toEqual(["direct"]);
  expect(menu()).toBeNull();
  expect(document.activeElement).toBe(trigger());
});

test("keyboard: arrows move, Enter chooses, Escape closes without choosing", () => {
  key("ArrowDown");
  expect(menu()).not.toBeNull();
  key("ArrowDown");
  key("Enter");
  expect(changes).toEqual(["custom"]);
  key("ArrowUp");
  key("Home");
  key("Escape");
  expect(menu()).toBeNull();
  expect(changes).toEqual(["custom"]);
});

test("Escape is kept from the dialog around it", () => {
  const parentKeys: string[] = [];
  act(() => {
    root.render(
      <div onKeyDown={(event) => parentKeys.push(event.key)}>
        <Select id="sel" value={value} options={options} onChange={() => {}} />
      </div>,
    );
  });
  key("Enter");
  const escape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  act(() => { trigger().dispatchEvent(escape); });
  expect(menu()).toBeNull();
  // A native <dialog> closes on an Escape keydown that was not cancelled.
  expect(escape.defaultPrevented).toBe(true);
  expect(parentKeys).toEqual(["Enter"]);
});

test("typing picks a matching option while closed", () => {
  key("d");
  expect(changes).toEqual(["direct"]);
});

test("a press outside closes the menu", () => {
  act(() => trigger().click());
  act(() => { document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })); });
  expect(menu()).toBeNull();
  expect(changes).toEqual([]);
});
