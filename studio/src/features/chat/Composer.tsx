import { useEffect, useId, useRef, useState } from "react";
import {
  ArrowUp,
  Square,
  Plus,
  Mic,
  LoaderCircle,
  X,
  FileText,
  AudioLines,
  MoreHorizontal,
} from "lucide-react";
import type { Attachment, Bot, Capabilities } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import ModelPicker, { PresenceSurface, useDialogFocus } from "./ModelPicker";
import ContextControl from "./ContextControl";
import type { useBotContext } from "../../hooks/useBotContext";
import {
  AnimatePresence, m, useIsPresent, useReducedMotion,
  controlMotion, fade, rowMotion, motionSpring, motionTransition,
} from "../../lib/motion";
import "./minimal-composer.css";
interface Upload {
  key: string;
  name: string;
  attachment?: Attachment;
  error?: string;
}
function initialDraft(scope: string, id: string) {
  if (!scope) return "";
  try {
    return localStorage.getItem(`connect-bots:draft:${scope}:${id}`) || "";
  } catch {
    return "";
  }
}
export default function Composer({
  bot,
  draftScope,
  capabilities,
  busy,
  onSend,
  onStop,
  onBotChange,
  onError,
  context,
  suspended = false,
}: {
  bot: Bot;
  draftScope: string;
  capabilities: Capabilities | null;
  busy: boolean;
  onSend: (text: string, attachments: Attachment[]) => Promise<void>;
  onStop: () => Promise<void>;
  onBotChange: (bot: Bot) => void;
  onError: (error: string) => void;
  context: ReturnType<typeof useBotContext>;
  suspended?: boolean;
}) {
  const present = useIsPresent();
  const reducedMotion = useReducedMotion();
  const [text, setText] = useState(() => initialDraft(draftScope, bot.id));
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [sending, setSending] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const [recording, setRecording] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  const [actionsOpen, setActionsOpen] = useState(false);
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  const actionsId = useId();
  const input = useRef<HTMLInputElement>(null);
  const audioInput = useRef<HTMLInputElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const textRevision = useRef(0);
  const actionsTrigger = useRef<HTMLButtonElement>(null);
  const actionsDialog = useRef<HTMLDivElement>(null);
  const recorder = useRef<MediaRecorder | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const audioChunks = useRef<Blob[]>([]);
  const microphoneLifecycle = useRef({ epoch: 0, active: true, present, draftScope, botId: bot.id });
  microphoneLifecycle.current.present = present;
  microphoneLifecycle.current.draftScope = draftScope;
  microphoneLifecycle.current.botId = bot.id;
  function closeActions() {
    setActionsOpen(false);
    setModelPickerOpen(false);
    requestAnimationFrame(() => {
      if (present) actionsTrigger.current?.focus();
    });
  }
  useDialogFocus(actionsOpen && !modelPickerOpen && present && !suspended, actionsDialog, closeActions);
  useEffect(() => {
    if (!suspended) return;
    setActionsOpen(false);
    setModelPickerOpen(false);
  }, [suspended]);
  useEffect(() => {
    try {
      if (draftScope) localStorage.setItem(`connect-bots:draft:${draftScope}:${bot.id}`, text);
    } catch {
      /* Private browsers may disable persistence. */
    }
    if (textarea.current && !recording) {
      textarea.current.style.height = "auto";
      if (text) {
        textarea.current.style.height = `${Math.min(textarea.current.scrollHeight, 180)}px`;
        textarea.current.style.overflowY = textarea.current.scrollHeight > 180 ? "auto" : "hidden";
      } else {
        textarea.current.style.overflowY = "hidden";
      }
    }
  }, [text, bot.id, draftScope, recording]);
  useEffect(() => {
    if (!recording) return;
    setElapsed(0);
    const timer = setInterval(() => setElapsed((t) => t + 1), 1000);
    return () => clearInterval(timer);
  }, [recording]);
  useEffect(() => {
    const lifecycle = microphoneLifecycle.current;
    lifecycle.epoch++;
    lifecycle.active = present;
    return () => {
      lifecycle.active = false;
      lifecycle.epoch++;
      if (recorder.current?.state === "recording") {
        recorder.current.onstop = null;
        recorder.current.stop();
      }
      stream.current?.getTracks().forEach((track) => track.stop());
    };
  }, [draftScope, bot.id, present]);
  const pendingUploads = uploads.some(
    (upload) => !upload.attachment && !upload.error,
  );
  async function send() {
    if (
      !present ||
      suspended ||
      sending ||
      busy ||
      context.compacting || context.requesting ||
      pendingUploads ||
      transcribing ||
      recording ||
      (!text.trim() && !uploads.some((u) => u.attachment))
    )
      return;
    const submittedText = text;
    const submittedRevision = textRevision.current;
    const submittedUploads = uploads.filter((upload) => upload.attachment);
    const submittedKeys = new Set(submittedUploads.map((upload) => upload.key));
    setSending(true);
    try {
      await onSend(
        submittedText.trim(),
        submittedUploads.flatMap((upload) =>
          upload.attachment ? [upload.attachment] : [],
        ),
      );
      // Acknowledging this message must not discard the next draft prepared
      // while the request was in flight, even if its text was edited back.
      setText((current) => textRevision.current === submittedRevision ? "" : current);
      setUploads((current) => current.filter((upload) => !submittedKeys.has(upload.key)));
      textarea.current?.focus();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setSending(false);
    }
  }
  async function files(files: FileList | null) {
    if (!files) return;
    for (const file of Array.from(files)) {
      const key =
        window.crypto?.randomUUID?.() ||
        `upload-${Date.now()}-${Math.random().toString(36).slice(2)}`;
      setUploads((current) => [...current, { key, name: file.name }]);
      try {
        const attachment = await api.upload(bot.id, file);
        setUploads((current) =>
          current.map((upload) =>
            upload.key === key ? { ...upload, attachment } : upload,
          ),
        );
      } catch (error) {
        setUploads((current) =>
          current.map((upload) =>
            upload.key === key
              ? { ...upload, error: errorMessage(error) }
              : upload,
          ),
        );
        onError(errorMessage(error));
      }
    }
    if (input.current) input.current.value = "";
  }
  async function transcribe(file: Blob, name?: string) {
    setTranscribing(true);
    try {
      const result = await api.transcribe(file, name);
      textRevision.current += 1;
      setText((current) => `${current}${current ? "\n" : ""}${result.text}`);
      textarea.current?.focus();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setTranscribing(false);
    }
  }
  async function microphone() {
    if (recording) {
      recorder.current?.stop();
      setRecording(false);
      return;
    }
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
      audioInput.current?.click();
      onError(
        "The microphone requires HTTPS. You can choose an audio file for transcription.",
      );
      return;
    }
    const lifecycle = microphoneLifecycle.current;
    const epoch = lifecycle.epoch;
    const current = () => lifecycle.active && lifecycle.present && lifecycle.epoch === epoch
      && lifecycle.draftScope === draftScope && lifecycle.botId === bot.id;
    try {
      const grantedStream = await navigator.mediaDevices.getUserMedia({
        audio: true,
      });
      // Permission can resolve after sign-out, a bot switch, or an exit
      // animation. The old composer must never start an invisible recorder.
      if (!current()) {
        grantedStream.getTracks().forEach((track) => track.stop());
        return;
      }
      stream.current = grantedStream;
      const mime = ["audio/webm;codecs=opus", "audio/mp4", "audio/webm"].find(
        (type) => MediaRecorder.isTypeSupported(type),
      );
      const media = new MediaRecorder(
        stream.current,
        mime ? { mimeType: mime } : undefined,
      );
      recorder.current = media;
      audioChunks.current = [];
      media.ondataavailable = (event) => {
        if (event.data.size) audioChunks.current.push(event.data);
      };
      media.onstop = () => {
        stream.current?.getTracks().forEach((track) => track.stop());
        stream.current = null;
        const blob = new Blob(audioChunks.current, { type: media.mimeType });
        setRecording(false);
        if (blob.size)
          void transcribe(
            blob,
            media.mimeType.includes("mp4") ? "dictation.m4a" : "dictation.webm",
          );
      };
      media.start();
      setRecording(true);
    } catch (error) {
      if (!current()) return;
      stream.current?.getTracks().forEach((track) => track.stop());
      onError(
        error instanceof DOMException && error.name === "NotAllowedError"
          ? "Microphone access was denied."
          : errorMessage(error),
      );
    }
  }
  async function stop() {
    setStopping(true);
    try {
      await onStop();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setStopping(false);
    }
  }
  return (
    <div className="composer-wrap composer-minimal">
      <div
        className={`composer ${recording ? "is-recording" : ""}`}
        onDragOver={(event) => {
          if (event.dataTransfer.types.includes("Files"))
            event.preventDefault();
        }}
        onDrop={(event) => {
          if (event.dataTransfer.files.length) {
            event.preventDefault();
            void files(event.dataTransfer.files);
          }
        }}
      >
        <AnimatePresence initial={false}>
        {uploads.length > 0 && (
          <PresenceSurface
            key="uploads"
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            transition={reducedMotion ? { duration: 0 } : motionTransition.disclosure}
            style={{ overflow: "hidden", minHeight: 0 }}
          >
          <div className="upload-chips">
            <AnimatePresence initial={false}>
            {uploads.map((upload) => (
              <PresenceSurface
                layout="position"
                variants={reducedMotion ? fade : rowMotion}
                initial="hidden"
                animate="visible"
                exit="exit"
                transition={{ layout: motionSpring.layout }}
                className={`upload-chip ${upload.error ? "is-error" : ""}`}
                key={upload.key}
              >
                {!upload.attachment && !upload.error ? (
                  <LoaderCircle size={14} className="spin" />
                ) : (
                  <FileText size={14} />
                )}
                <span title={upload.error || upload.name}>{upload.name}</span>
                <m.button
                  {...controlMotion}
                  onClick={() =>
                    setUploads((current) =>
                      current.filter((u) => u.key !== upload.key),
                    )
                  }
                  aria-label={`Remove ${upload.name}`}
                >
                  <X size={13} />
                </m.button>
              </PresenceSurface>
            ))}
            </AnimatePresence>
          </div>
          </PresenceSurface>
        )}
        </AnimatePresence>
        <AnimatePresence initial={false}>
        {recording && (
          <PresenceSurface
            key="recording"
            className="recording-state"
            initial={{ opacity: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}
            animate={{ opacity: 1, height: "auto", paddingTop: 2, paddingBottom: 4 }}
            exit={{ opacity: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}
            transition={reducedMotion ? { duration: 0 } : motionTransition.disclosure}
            style={{ overflow: "hidden", minHeight: 0 }}
          >
            <m.span
              className="recording-dot"
              animate={reducedMotion ? { opacity: 1 } : { opacity: [1, 0.4, 1] }}
              transition={reducedMotion ? { duration: 0 } : { duration: 1.25, repeat: Infinity, ease: "easeInOut" }}
            />
            <AudioLines size={19} />
            <span>Recording voice</span>
            <time>
              {Math.floor(elapsed / 60)}:{String(elapsed % 60).padStart(2, "0")}
            </time>
            <m.button
              {...controlMotion}
              className="text-button"
              onClick={() => {
                if (recorder.current) {
                  recorder.current.onstop = () =>
                    stream.current
                      ?.getTracks()
                      .forEach((track) => track.stop());
                  recorder.current.stop();
                }
                setRecording(false);
              }}
            >
              Cancel
            </m.button>
          </PresenceSurface>
        )}
        </AnimatePresence>
          <textarea
            ref={textarea}
            hidden={recording}
            style={{ display: recording ? "none" : undefined }}
            value={text}
            onChange={(event) => {
              textRevision.current += 1;
              setText(event.target.value);
            }}
            onKeyDown={(event) => {
              if (
                event.key === "Enter" &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing &&
                window.matchMedia("(pointer: fine)").matches
              ) {
                event.preventDefault();
                void send();
              }
            }}
            onPaste={(event) => {
              if (event.clipboardData.files.length) {
                event.preventDefault();
                void files(event.clipboardData.files);
              }
            }}
            placeholder={
              transcribing
                ? "Transcribing your voice…"
                  : busy
                  ? "Prepare your next message…"
                  : context.compacting
                    ? "Compacting context…"
                  : `Message ${bot.name}`
            }
            aria-label={`Message ${bot.name}`}
            rows={1}
            disabled={transcribing}
          />
        <div className="composer-toolbar">
          <div className="composer-tools">
            <m.button
              {...controlMotion}
              className="icon-button"
              onClick={() => input.current?.click()}
              aria-label="Attach file"
              title="Attach file"
            >
              {pendingUploads ? <LoaderCircle size={18} className="spin" /> : <Plus size={19} />}
            </m.button>
            {capabilities?.voice && (
              <m.button
                {...controlMotion}
                className={`icon-button ${recording ? "is-recording" : ""}`}
                onClick={() => void microphone()}
                disabled={transcribing || sending}
                aria-label={recording ? "Finish recording" : "Dictation"}
                title={
                  window.isSecureContext
                    ? "Dictation"
                    : "Choose an audio file · the microphone requires HTTPS"
                }
              >
                {transcribing ? (
                  <LoaderCircle size={17} className="spin" />
                ) : recording ? (
                  <Square size={16} />
                ) : (
                  <Mic size={18} />
                )}
              </m.button>
            )}
          </div>
          <div className="composer-trailing">
            <ContextControl state={context} busy={busy || sending} suspended={suspended} onError={onError} />
            <div className="composer-actions">
              <m.button
                {...controlMotion}
                ref={actionsTrigger}
                className="composer-actions-trigger"
                onClick={() => setActionsOpen(!actionsOpen)}
                aria-label="More actions"
                aria-expanded={actionsOpen}
                aria-haspopup="dialog"
                aria-controls={actionsOpen ? actionsId : undefined}
                title="More actions"
              ><MoreHorizontal size={19} /></m.button>
              {actionsOpen && present && !suspended && (
                <button className="popover-backdrop" tabIndex={-1} onClick={closeActions} aria-label="Close actions" />
              )}
              <AnimatePresence initial={false}>
                {actionsOpen && present && !suspended && <PresenceSurface
                  key="actions"
                  variants={fade}
                  initial="hidden" animate="visible" exit="exit"
                  className="composer-actions-menu" ref={actionsDialog} tabIndex={-1} id={actionsId} role="dialog" modal={!modelPickerOpen} aria-label="Message actions"
                >
                  <ModelPicker
                    bot={bot}
                    capabilities={capabilities}
                    disabled={busy || sending || context.compacting || context.requesting}
                    onBotChange={onBotChange}
                    onOpenChange={setModelPickerOpen}
                    onError={onError}
                  />
                  {capabilities?.voice && <m.button
                    {...controlMotion}
                    onClick={() => {
                      closeActions();
                      audioInput.current?.click();
                    }}
                    disabled={transcribing}
                  ><AudioLines size={16} /><span>Transcribe audio file</span></m.button>}
                </PresenceSurface>}
              </AnimatePresence>
            </div>
            <m.button
              {...controlMotion}
              className={`send-button ${busy ? "stop-button" : ""}`.trim()}
              onClick={() => void (busy ? stop() : send())}
              disabled={busy ? stopping :
                sending ||
                context.compacting || context.requesting ||
                pendingUploads ||
                transcribing ||
                recording ||
                (!text.trim() && !uploads.some((u) => u.attachment))
              }
              aria-label={busy ? "Stop bot" : "Send message"}
              title={busy ? "Stop" : "Send"}
            >
              <span className="composer-send-icon">
                <AnimatePresence initial={false} mode="popLayout">
                  <m.span
                    key={busy ? stopping ? "stopping" : "stop" : sending ? "sending" : "send"}
                    initial={{ opacity: 0, scale: reducedMotion ? 1 : 0.7, rotate: reducedMotion ? 0 : -12 }}
                    animate={{ opacity: 1, scale: 1, rotate: 0 }}
                    exit={{ opacity: 0, scale: reducedMotion ? 1 : 0.7, rotate: reducedMotion ? 0 : 12 }}
                    transition={reducedMotion ? { duration: 0 } : motionTransition.quick}
                  >
                    {busy ? stopping ? <LoaderCircle size={16} className="spin" /> : <Square size={14} fill="currentColor" /> : sending ? <LoaderCircle size={16} className="spin" /> : <ArrowUp size={20} />}
                  </m.span>
                </AnimatePresence>
              </span>
            </m.button>
          </div>
        </div>
      </div>
      {(transcribing || pendingUploads) && <span className="composer-live-status" role="status">
        {transcribing ? "Transcribing voice…" : "Uploading files…"}
      </span>}
      <input
        hidden
        type="file"
        multiple
        ref={input}
        onChange={(e) => void files(e.target.files)}
      />
      <input
        hidden
        type="file"
        accept="audio/*,.m4a,.wav,.mp3,.ogg,.webm"
        ref={audioInput}
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void transcribe(file, file.name);
          e.target.value = "";
        }}
      />
    </div>
  );
}
