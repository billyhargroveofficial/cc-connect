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
import ModelPicker, { useDialogFocus } from "./ModelPicker";
import ContextControl from "./ContextControl";
import type { useBotContext } from "../../hooks/useBotContext";
import "./minimal-composer.css";
interface Upload {
  key: string;
  name: string;
  attachment?: Attachment;
  error?: string;
}
function initialDraft(id: string) {
  try {
    return localStorage.getItem(`connect-bots:draft:${id}`) || "";
  } catch {
    return "";
  }
}
export default function Composer({
  bot,
  capabilities,
  busy,
  onSend,
  onStop,
  onBotChange,
  onError,
  context,
}: {
  bot: Bot;
  capabilities: Capabilities | null;
  busy: boolean;
  onSend: (text: string, attachments: Attachment[]) => Promise<void>;
  onStop: () => Promise<void>;
  onBotChange: (bot: Bot) => void;
  onError: (error: string) => void;
  context: ReturnType<typeof useBotContext>;
}) {
  const [text, setText] = useState(() => initialDraft(bot.id));
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
  function closeActions() {
    setActionsOpen(false);
    setModelPickerOpen(false);
    requestAnimationFrame(() => actionsTrigger.current?.focus());
  }
  useDialogFocus(actionsOpen && !modelPickerOpen, actionsDialog, closeActions);
  useEffect(() => {
    try {
      localStorage.setItem(`connect-bots:draft:${bot.id}`, text);
    } catch {
      /* Private browsers may disable persistence. */
    }
    if (textarea.current) {
      textarea.current.style.height = "auto";
      if (text) {
        textarea.current.style.height = `${Math.min(textarea.current.scrollHeight, 180)}px`;
        textarea.current.style.overflowY = textarea.current.scrollHeight > 180 ? "auto" : "hidden";
      } else {
        textarea.current.style.overflowY = "hidden";
      }
    }
  }, [text, bot.id]);
  useEffect(() => {
    if (!recording) return;
    setElapsed(0);
    const timer = setInterval(() => setElapsed((t) => t + 1), 1000);
    return () => clearInterval(timer);
  }, [recording]);
  useEffect(
    () => () => {
      if (recorder.current?.state === "recording") {
        recorder.current.onstop = null;
        recorder.current.stop();
      }
      stream.current?.getTracks().forEach((track) => track.stop());
    },
    [],
  );
  const pendingUploads = uploads.some(
    (upload) => !upload.attachment && !upload.error,
  );
  async function send() {
    if (
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
        "Для микрофона нужен HTTPS. Можно выбрать аудиофайл для диктовки.",
      );
      return;
    }
    try {
      stream.current = await navigator.mediaDevices.getUserMedia({
        audio: true,
      });
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
      stream.current?.getTracks().forEach((track) => track.stop());
      onError(
        error instanceof DOMException && error.name === "NotAllowedError"
          ? "Доступ к микрофону не разрешён."
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
        {uploads.length > 0 && (
          <div className="upload-chips">
            {uploads.map((upload) => (
              <div
                className={`upload-chip ${upload.error ? "is-error" : ""}`}
                key={upload.key}
              >
                {!upload.attachment && !upload.error ? (
                  <LoaderCircle size={14} className="spin" />
                ) : (
                  <FileText size={14} />
                )}
                <span title={upload.error || upload.name}>{upload.name}</span>
                <button
                  onClick={() =>
                    setUploads((current) =>
                      current.filter((u) => u.key !== upload.key),
                    )
                  }
                  aria-label={`Убрать ${upload.name}`}
                >
                  <X size={13} />
                </button>
              </div>
            ))}
          </div>
        )}
        {recording ? (
          <div className="recording-state">
            <span className="recording-dot" />
            <AudioLines size={19} />
            <span>Записываем голос</span>
            <time>
              {Math.floor(elapsed / 60)}:{String(elapsed % 60).padStart(2, "0")}
            </time>
            <button
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
              Отменить
            </button>
          </div>
        ) : (
          <textarea
            ref={textarea}
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
                ? "Распознаём вашу речь…"
                  : busy
                  ? "Подготовьте следующее сообщение…"
                  : context.compacting
                    ? "Контекст сжимается…"
                  : `Напишите ${bot.name}`
            }
            aria-label={`Сообщение боту ${bot.name}`}
            rows={1}
            disabled={transcribing}
          />
        )}
        <div className="composer-toolbar">
          <div className="composer-tools">
            <button
              className="icon-button"
              onClick={() => input.current?.click()}
              aria-label="Прикрепить файл"
              title="Прикрепить файл"
            >
              {pendingUploads ? <LoaderCircle size={18} className="spin" /> : <Plus size={19} />}
            </button>
            {capabilities?.voice && (
              <button
                className={`icon-button ${recording ? "is-recording" : ""}`}
                onClick={() => void microphone()}
                disabled={transcribing || sending}
                aria-label={recording ? "Завершить запись" : "Диктовка"}
                title={
                  window.isSecureContext
                    ? "Диктовка"
                    : "Выбрать аудиофайл · для микрофона нужен HTTPS"
                }
              >
                {transcribing ? (
                  <LoaderCircle size={17} className="spin" />
                ) : recording ? (
                  <Square size={16} />
                ) : (
                  <Mic size={18} />
                )}
              </button>
            )}
          </div>
          <div className="composer-trailing">
            <ContextControl state={context} busy={busy || sending} onError={onError} />
            <div className="composer-actions">
              <button
                ref={actionsTrigger}
                className="composer-actions-trigger"
                onClick={() => setActionsOpen(!actionsOpen)}
                aria-label="Ещё действия"
                aria-expanded={actionsOpen}
                aria-haspopup="dialog"
                aria-controls={actionsOpen ? actionsId : undefined}
                title="Ещё действия"
              ><MoreHorizontal size={19} /></button>
              {actionsOpen && <>
                <button className="popover-backdrop" tabIndex={-1} onClick={closeActions} aria-label="Закрыть действия" />
                <div className="composer-actions-menu" ref={actionsDialog} tabIndex={-1} id={actionsId} role="dialog" aria-modal={!modelPickerOpen} aria-label="Действия с сообщением">
                  <ModelPicker
                    bot={bot}
                    capabilities={capabilities}
                    disabled={busy || sending || context.compacting || context.requesting}
                    onBotChange={onBotChange}
                    onOpenChange={setModelPickerOpen}
                    onError={onError}
                  />
                  {capabilities?.voice && <button
                    onClick={() => {
                      closeActions();
                      audioInput.current?.click();
                    }}
                    disabled={transcribing}
                  ><AudioLines size={16} /><span>Распознать аудиофайл</span></button>}
                </div>
              </>}
            </div>
          {busy ? (
            <button
              className="send-button stop-button"
              onClick={() => void stop()}
              disabled={stopping}
              aria-label="Остановить бота"
              title="Остановить"
            >
              {stopping ? (
                <LoaderCircle size={16} className="spin" />
              ) : (
                <Square size={14} fill="currentColor" />
              )}
            </button>
          ) : (
            <button
              className="send-button"
              onClick={() => void send()}
              disabled={
                sending ||
                context.compacting || context.requesting ||
                pendingUploads ||
                transcribing ||
                recording ||
                (!text.trim() && !uploads.some((u) => u.attachment))
              }
              aria-label="Отправить сообщение"
              title="Отправить"
            >
              {sending ? (
                <LoaderCircle size={16} className="spin" />
              ) : (
                <ArrowUp size={20} />
              )}
            </button>
          )}
          </div>
        </div>
      </div>
      {(transcribing || pendingUploads) && <span className="composer-live-status" role="status">
        {transcribing ? "Распознаём речь…" : "Загружаем файлы…"}
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
