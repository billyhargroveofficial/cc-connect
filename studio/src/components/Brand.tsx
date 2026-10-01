export default function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <span className="brand">
      <span className="brand-symbol" aria-hidden="true">
        <svg viewBox="0 0 28 28">
          <path
            d="M7 7h5v5H7zM16 7h5v5h-5zM7 16h5v5H7zM16 16h5v5h-5z"
            fill="currentColor"
          />
          <path
            d="M12 9.5h4M9.5 12v4M18.5 12v4M12 18.5h4"
            stroke="currentColor"
            strokeWidth="1.5"
          />
        </svg>
      </span>
      {!compact && (
        <span>
          Connect <span className="brand-light">Bots</span>
        </span>
      )}
    </span>
  );
}
