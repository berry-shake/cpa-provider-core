import { useEffect, useLayoutEffect, useRef } from "react";
import { components } from "@provider/components";
import { plugin } from "@provider/plugin";
import type { T } from "../../i18n";
import { defaultPrefix } from "../../lib/credentials";
import {
  currentPrefix, newEditor, pickableModels, pickerRows, previewSample, registerPreview, type EditorState, type ProxyChoice,
} from "../../lib/editor";
import { store, type State } from "../../state/store";
import { Select } from "../Select";

function Picker({ t, s, ed }: { t: T; s: State; ed: EditorState }) {
  const all = ed.all;
  const models = s.catalog && Array.isArray(s.catalog.models) ? s.catalog.models : [];
  const offered = pickableModels(models, ed);
  const total = offered.length;
  let list;
  if (!s.catalog) {
    list = (
      <div className={"empty" + (s.catalogError ? " danger-text" : "")}>
        {s.catalogError ? t("picker.error", { msg: s.catalogError }) : t("picker.loading")}
      </div>
    );
  } else {
    const rows = pickerRows(offered, ed.selected, ed.filter);
    list = (
      <>
        {rows.length === 0 ? <div className="empty">{t("picker.noMatch")}</div> : null}
        {rows.map((r) => {
          const name = r.missing ? { text: t("picker.missing"), title: "" } : plugin.editor.pickName(t, r);
          return (
            <label key={(r.missing ? "missing:" : "model:") + r.id} className={"pick-row" + (r.missing ? " missing" : "")}>
              <input
                type="checkbox"
                checked={all || ed.selected.includes(r.id)}
                disabled={all}
                onChange={(event) => store.pickModel(r.id, event.target.checked)}
              />
              <span className="mono">{r.id}</span>
              <span className="pick-name" title={name.title}>{name.text}</span>
              <span className="pick-meta">{r.missing ? "" : plugin.editor.modelMeta(t, r)}</span>
            </label>
          );
        })}
      </>
    );
  }
  return (
    <div className={"picker" + (all ? " disabled" : "")} id="picker">
      <div className="picker-bar">
        <input
          className="input"
          id="picker-search"
          type="search"
          spellCheck={false}
          placeholder={t("picker.search")}
          value={ed.filter}
          onChange={(event) => store.updateEditor({ filter: event.target.value })}
        />
        <button className="btn btn-secondary btn-sm" type="button" id="picker-select" disabled={all} onClick={() => store.selectShownModels()}>
          {t("picker.selectVisible")}
        </button>
        <button className="btn btn-secondary btn-sm" type="button" id="picker-clear" disabled={all} onClick={() => store.clearPickedModels()}>
          {t("picker.clear")}
        </button>
        <span className="picker-count" id="picker-count" title={plugin.editor.countTip ? plugin.editor.countTip(t, ed) : undefined}>
          {all ? t("picker.countAll", { total }) : t("picker.count", { selected: ed.selected.length, total })}
        </span>
      </div>
      <div className="picker-list" id="picker-list">{list}</div>
    </div>
  );
}

/**
 * The egress select: CPA's built-in choices, the saved proxies, or an
 * address typed in.
 */
function ProxyField({ t, s, ed }: { t: T; s: State; ed: EditorState }) {
  const proxies = s.proxies ? s.proxies.proxies : [];
  const global = s.proxies ? s.proxies.global_proxy : s.status && s.status.config ? s.status.config.global_proxy : undefined;
  const choice = ed.proxyChoice;
  // A saved list proxy stays selectable before the list loaded or after it was removed.
  const orphan = choice.startsWith("id:") && !proxies.some((p) => "id:" + p.id === choice);
  const orphanName = ed.original && ed.original.proxyChoice === choice ? ed.original.proxyName || ed.original.proxy : choice.slice(3);
  return (
    <div className="field">
      <label htmlFor="f-proxy-choice">{t("form.proxy")}</label>
      <Select<ProxyChoice>
        id="f-proxy-choice"
        value={choice}
        onChange={(proxyChoice) => store.updateEditor({ proxyChoice })}
        options={[
          { value: "", label: global ? t("form.proxyInheritValue", { proxy: global }) : t("form.proxyInherit") },
          { value: "direct", label: t("form.proxyDirect") },
          // A proxy without a name is shown by its address alone.
          ...proxies.map((p) => ({ value: ("id:" + p.id) as ProxyChoice, label: p.name || p.label || p.url, detail: p.name ? p.label || p.url : undefined })),
          ...(orphan ? [{ value: choice, label: orphanName }] : []),
          { value: "custom", label: t("form.proxyCustom") },
        ]}
      />
      {choice === "custom" ? (
        <input
          className="input mono"
          id="f-proxy"
          type="text"
          spellCheck={false}
          autoFocus
          placeholder="socks5://user:pass@host:1080"
          value={ed.proxy}
          onChange={(event) => store.updateEditor({ proxy: event.target.value })}
        />
      ) : null}
      <span className="help">{t(choice === "custom" ? "form.proxyCustomHelp" : "form.proxyHelp")}</span>
    </div>
  );
}

/**
 * The add/edit dialog. It stays mounted; opening a new editor (a new seq)
 * shows it and focuses the first field, and Esc closes it natively.
 */
export function EditorDialog({ t, s }: { t: T; s: State }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const keyInput = useRef<HTMLInputElement>(null);
  const labelInput = useRef<HTMLInputElement>(null);
  // While closed the dialog keeps showing the last form, minus the secrets:
  // the key, a custom proxy address (which may carry a password) and what
  // plugin.editor.clearSecrets blanks.
  const last = useRef<EditorState | null>(null);
  if (s.editor) last.current = s.editor;
  else if (last.current) {
    const blank = plugin.editor.clearSecrets ? plugin.editor.clearSecrets(last.current) : null;
    if (last.current.key || last.current.proxy || blank) last.current = { ...last.current, ...blank, key: "", proxy: "" };
  }
  const ed = s.editor || last.current || newEditor(0, "add", null, defaultPrefix(s.status));
  const seq = s.editor ? s.editor.seq : 0;

  useLayoutEffect(() => {
    const box = dialog.current;
    if (!box) return;
    if (!seq) {
      if (box.open) box.close();
      return;
    }
    if (!box.open) box.showModal();
    (store.getState().editor?.mode === "edit" ? labelInput : keyInput).current?.focus();
  }, [seq]);

  // Esc closes the dialog natively; drop the half-filled form with it. A
  // reopen right after close() must not be wiped by the queued event.
  useEffect(() => {
    const box = dialog.current;
    if (!box) return;
    const onClose = () => {
      if (!box.open) store.closeEditor();
    };
    box.addEventListener("close", onClose);
    return () => box.removeEventListener("close", onClose);
  }, []);

  const adding = ed.mode === "add";
  const dp = defaultPrefix(s.status);
  const models = s.catalog && Array.isArray(s.catalog.models) ? s.catalog.models : [];
  const forced = !!(s.status && s.status.config && s.status.config.force_model_prefix);
  const preview = registerPreview(t, currentPrefix(ed, dp), previewSample(ed.selected, models), forced);
  const Fields = components.EditorFields;

  return (
    <dialog className="modal" id="editor-dialog" aria-labelledby="editor-title" ref={dialog}>
      <form
        className="modal-form"
        id="editor-form"
        autoComplete="off"
        onSubmit={(event) => {
          event.preventDefault();
          void store.saveEditor();
        }}
      >
        <div className="modal-head">
          <div>
            <h2 id="editor-title">{adding ? t("form.addTitle") : t("form.editTitle", { name: ed.title })}</h2>
            <p className="sub" id="editor-hint">{adding ? t("form.addHint") : t("form.editHint")}</p>
          </div>
          <button className="icon-btn" type="button" id="editor-close" aria-label={t("form.close")} title={t("form.close")} onClick={() => store.closeEditor()}>
            ×
          </button>
        </div>
        <div className="modal-body">
          <div className="form-grid">
            <div className={"field" + (adding ? "" : " hidden")} id="row-key">
              <label htmlFor="f-key">{t("form.key")}</label>
              <input
                ref={keyInput}
                className="input mono"
                id="f-key"
                type="text"
                autoComplete="off"
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                placeholder={t("form.keyPlaceholder")}
                value={ed.key}
                onChange={(event) => store.updateEditor({ key: event.target.value })}
              />
              <label className="check">
                <input type="checkbox" id="f-skip-verify" checked={ed.skipVerify} onChange={(event) => store.updateEditor({ skipVerify: event.target.checked })} />{" "}
                <span>{t("form.skipVerify")}</span>
              </label>
            </div>
            <div className="field">
              <label htmlFor="f-label">{t("form.label")}</label>
              <input
                ref={labelInput}
                className="input"
                id="f-label"
                type="text"
                maxLength={120}
                placeholder={ed.labelPlaceholder}
                value={ed.label}
                onChange={(event) => store.updateEditor({ label: event.target.value })}
              />
              <span className="help">{t("form.labelHelp")}</span>
            </div>
            <div className="field">
              <label htmlFor="f-note">{t("form.note")}</label>
              <input
                className="input"
                id="f-note"
                type="text"
                maxLength={200}
                placeholder={t("form.notePlaceholder")}
                value={ed.note}
                onChange={(event) => store.updateEditor({ note: event.target.value })}
              />
              <span className="help">{t("form.noteHelp")}</span>
            </div>
            <ProxyField t={t} s={s} ed={ed} />
            <div className="field">
              <label htmlFor="f-prefix">{t("form.prefix")}</label>
              <input
                className="input mono"
                id="f-prefix"
                type="text"
                spellCheck={false}
                disabled={ed.prefixDefault}
                value={ed.prefixDefault ? dp : ed.prefix}
                onChange={(event) => store.updateEditor({ prefix: event.target.value })}
              />
              <label className="check">
                <input type="checkbox" id="f-prefix-default" checked={ed.prefixDefault} onChange={(event) => store.setPrefixDefault(event.target.checked)} />{" "}
                <span id="f-prefix-default-label">{dp ? t("form.prefixDefault", { prefix: dp }) : t("form.prefixDefaultNone")}</span>
              </label>
              <span className="help">{t("form.prefixHelp")}</span>
            </div>
            <div className="field">
              <label htmlFor="f-priority">{t("form.priority")}</label>
              <input
                className="input"
                id="f-priority"
                type="number"
                step={1}
                inputMode="numeric"
                value={ed.priority}
                onChange={(event) => store.updateEditor({ priority: event.target.value })}
              />
              <span className="help">{t("form.priorityHelp")}</span>
            </div>
            {Fields ? <Fields t={t} s={s} ed={ed} /> : null}
          </div>
          <div className="models-mode" role="radiogroup">
            <label className="check">
              <input type="radio" name="models-mode" id="f-models-all" value="all" checked={ed.all} onChange={(event) => event.target.checked && store.updateEditor({ all: true })} />{" "}
              <span>{t("form.modelsAll")}</span>
            </label>
            <label className="check">
              <input type="radio" name="models-mode" id="f-models-pick" value="pick" checked={!ed.all} onChange={(event) => event.target.checked && store.updateEditor({ all: false })} />{" "}
              <span>{t("form.modelsPick")}</span>
            </label>
          </div>
          <Picker t={t} s={s} ed={ed} />
          <p className="sub" id="register-preview">{preview}</p>
        </div>
        <div className="modal-foot">
          <span className={"form-error" + (ed.error ? "" : " hidden")} id="editor-error" role="alert">
            {ed.error ? t(ed.error.key, ed.error.params) : ""}
          </span>
          <button className="btn btn-secondary" type="button" id="editor-cancel" onClick={() => store.closeEditor()}>
            {t("form.cancel")}
          </button>
          <button className="btn btn-primary" type="submit" id="editor-save" disabled={!!s.busy.save}>
            {adding ? t("form.add") : t("form.save")}
          </button>
        </div>
      </form>
    </dialog>
  );
}
