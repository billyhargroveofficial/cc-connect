import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { LazyMotion, MotionConfig } from "framer-motion";
import App from "./App";
import "./styles.css";
import "katex/dist/katex.min.css";

const loadMotionFeatures = () => import("./lib/motion-features").then((module) => module.default);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MotionConfig reducedMotion="user">
      <LazyMotion features={loadMotionFeatures} strict>
        <App />
      </LazyMotion>
    </MotionConfig>
  </StrictMode>,
);
