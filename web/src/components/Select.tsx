import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";

/** One choice of a Select. */
export interface SelectOption<V extends string = string> {
  value: V;
  /** The text shown on the button and in the menu, also what typing matches. */
  label: string;
  /** Muted text after the label (a proxy's address). */
  detail?: string;
}

export interface SelectProps<V extends string = string> {
  /** The button's id, for a <label htmlFor>. */
  id?: string;
  value: V;
  options: readonly SelectOption<V>[];
  onChange: (value: V) => void;
  /** The input-sm size of toolbars. */
  small?: boolean;
  "aria-label"?: string;
  disabled?: boolean;
}

/** Where the open menu sits, in viewport coordinates (it is position: fixed). */
interface MenuBox {
  left: number;
  width: number;
  top?: number;
  bottom?: number;
  maxHeight: number;
}

const MENU_GAP = 4;
const MENU_MAX = 320;
const VIEWPORT_MARGIN = 8;

/**
 * A select drawn by the page instead of the browser's native menu, following
 * the WAI-ARIA select-only combobox pattern: focus stays on the button, which
 * points at the active option with aria-activedescendant.
 *
 * The menu is position: fixed inside the component, not portaled: inside a
 * modal <dialog> anything outside the dialog is inert, and fixed positioning
 * is not clipped by the dialog's overflow.
 */
export function Select<V extends string = string>(props: SelectProps<V>) {
  const { id, value, options, onChange, small, disabled } = props;
  const autoId = useId();
  const buttonId = id || "select-" + autoId.replace(/:/g, "");
  const listId = buttonId + "-list";
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLUListElement>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [box, setBox] = useState<MenuBox | null>(null);
  const typed = useRef({ text: "", at: 0 });

  const selectedIndex = options.findIndex((option) => option.value === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;

  const place = useCallback(() => {
    const trigger = button.current;
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    const below = window.innerHeight - rect.bottom - MENU_GAP - VIEWPORT_MARGIN;
    const above = rect.top - MENU_GAP - VIEWPORT_MARGIN;
    const wanted = Math.min(MENU_MAX, menu.current ? menu.current.scrollHeight : MENU_MAX);
    const left = Math.max(VIEWPORT_MARGIN, Math.min(rect.left, window.innerWidth - VIEWPORT_MARGIN - rect.width));
    // Below when the menu fits there or there is no more room above; the
    // menu never grows past the room on its side, so it stays in view.
    if (below >= wanted || below >= above) {
      setBox({ left, width: rect.width, top: rect.bottom + MENU_GAP, maxHeight: Math.max(0, Math.min(MENU_MAX, below)) });
    } else {
      setBox({ left, width: rect.width, bottom: window.innerHeight - rect.top + MENU_GAP, maxHeight: Math.max(0, Math.min(MENU_MAX, above)) });
    }
  }, []);

  const show = (index: number) => {
    if (disabled || !options.length) return;
    setActive(index >= 0 ? index : 0);
    setOpen(true);
    // Safari does not focus a clicked button; the keys go to the button.
    button.current?.focus({ preventScroll: true });
  };
  const hide = (refocus: boolean) => {
    setOpen(false);
    setBox(null);
    if (refocus) button.current?.focus();
  };
  const choose = (index: number) => {
    const option = options[index];
    hide(true);
    if (option && option.value !== value) onChange(option.value);
  };

  useLayoutEffect(() => {
    if (open) place();
  }, [open, place, options.length]);

  // Follow the button while the page or the dialog scrolls; close on a press
  // anywhere else. Escape is caught before anything else sees it and
  // cancelled, so it closes the menu but not a <dialog> around it, wherever
  // the focus is.
  useEffect(() => {
    if (!open) return;
    const onEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      hide(true);
    };
    const onPress = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target && (button.current?.contains(target) || menu.current?.contains(target))) return;
      hide(false);
    };
    const onMove = (event: Event) => {
      if (event.target instanceof Node && menu.current?.contains(event.target)) return;
      place();
    };
    document.addEventListener("keydown", onEscape, true);
    document.addEventListener("pointerdown", onPress, true);
    window.addEventListener("resize", onMove);
    window.addEventListener("scroll", onMove, true);
    return () => {
      document.removeEventListener("keydown", onEscape, true);
      document.removeEventListener("pointerdown", onPress, true);
      window.removeEventListener("resize", onMove);
      window.removeEventListener("scroll", onMove, true);
    };
  }, [open, place]);

  // Keep the active option in view.
  useEffect(() => {
    if (!open || active < 0) return;
    const item = menu.current?.children[active] as HTMLElement | undefined;
    if (item && typeof item.scrollIntoView === "function") item.scrollIntoView({ block: "nearest" });
  }, [open, active, box]);

  /** Type-ahead: the next option whose label starts with what was typed. */
  const match = (key: string, from: number): number => {
    const now = Date.now();
    typed.current.text = now - typed.current.at > 700 ? key : typed.current.text + key;
    typed.current.at = now;
    const text = typed.current.text.toLowerCase();
    const start = typed.current.text.length > 1 ? from : from + 1;
    for (let step = 0; step < options.length; step++) {
      const index = (start + step) % options.length;
      if (options[index].label.toLowerCase().startsWith(text)) return index;
    }
    return -1;
  };

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    const last = options.length - 1;
    if (!open) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        show(selectedIndex);
      } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
        // Like a native select: typing picks without opening.
        const index = match(event.key, Math.max(selectedIndex, 0));
        if (index >= 0 && options[index].value !== value) onChange(options[index].value);
      }
      return;
    }
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActive((index) => Math.min(last, index + 1));
        return;
      case "ArrowUp":
        event.preventDefault();
        setActive((index) => Math.max(0, index - 1));
        return;
      case "Home":
      case "PageUp":
        event.preventDefault();
        setActive(0);
        return;
      case "End":
      case "PageDown":
        event.preventDefault();
        setActive(last);
        return;
      case "Enter":
      case " ":
        event.preventDefault();
        choose(active);
        return;
      case "Tab":
        hide(false);
        return;
    }
    if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      const index = match(event.key, active);
      if (index >= 0) setActive(index);
    }
  };

  const optionId = (index: number) => listId + "-" + index;
  const classes = ["input", "select-trigger"];
  if (small) classes.push("input-sm");
  if (open) classes.push("open");

  return (
    <div className="select">
      <button
        ref={button}
        type="button"
        className={classes.join(" ")}
        id={buttonId}
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        // The listbox exists only while open.
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && active >= 0 ? optionId(active) : undefined}
        aria-label={props["aria-label"]}
        disabled={disabled}
        onClick={() => (open ? hide(true) : show(selectedIndex))}
        onKeyDown={onKeyDown}
        onBlur={(event) => {
          if (open && !menu.current?.contains(event.relatedTarget as Node | null)) hide(false);
        }}
      >
        <span className="select-value">
          <span className="select-label">{selected ? selected.label : ""}</span>
          {selected && selected.detail ? <span className="select-detail">{selected.detail}</span> : null}
        </span>
      </button>
      {open ? (
        <ul
          ref={menu}
          className="select-menu"
          id={listId}
          role="listbox"
          aria-labelledby={buttonId}
          tabIndex={-1}
          style={box ? { left: box.left, minWidth: box.width, top: box.top, bottom: box.bottom, maxHeight: box.maxHeight } : { visibility: "hidden" }}
        >
          {options.map((option, index) => (
            <li
              key={option.value}
              id={optionId(index)}
              role="option"
              aria-selected={index === selectedIndex}
              className={"select-option" + (index === active ? " active" : "")}
              onPointerMove={() => setActive(index)}
              onPointerDown={(event) => event.preventDefault()}
              onClick={() => choose(index)}
            >
              <span className="select-check" aria-hidden="true" />
              <span className="select-label">{option.label}</span>
              {option.detail ? <span className="select-detail">{option.detail}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
