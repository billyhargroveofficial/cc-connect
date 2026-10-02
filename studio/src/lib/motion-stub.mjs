const host = new Proxy({}, { get: (_, name) => String(name) })

export function motionTestModule() {
  const passthrough = 'fragment'
  return {
    AnimatePresence: passthrough,
    LayoutGroup: passthrough,
    m: host,
    useIsPresent: () => true,
    useReducedMotion: () => false,
    easeOut: [0.16, 1, 0.3, 1],
    easeIn: [0.4, 0, 1, 1],
    motionTransition: {
      quick: {}, enter: {}, exit: {}, disclosure: {},
    },
    motionSpring: {
      control: {}, layout: {}, surface: {},
    },
    fade: {},
    fadeUp: {},
    rowMotion: {},
    popoverMotion: {},
    modalMotion: {},
    backdropMotion: {},
    controlMotion: {},
    softControlMotion: {},
  }
}
