import type { Transition, Variants } from 'framer-motion'

export {
  AnimatePresence,
  LayoutGroup,
  m,
  useIsPresent,
  useReducedMotion,
} from 'framer-motion'

export const easeOut = [0.16, 1, 0.3, 1] as const
export const easeIn = [0.4, 0, 1, 1] as const

export const motionTransition = {
  quick: { duration: 0.14, ease: easeOut },
  enter: { duration: 0.2, ease: easeOut },
  exit: { duration: 0.13, ease: easeIn },
  disclosure: { duration: 0.19, ease: easeOut },
} satisfies Record<string, Transition>

export const motionSpring = {
  control: { type: 'spring', stiffness: 520, damping: 34, mass: 0.55 },
  layout: { type: 'spring', stiffness: 420, damping: 38, mass: 0.78 },
  surface: { type: 'spring', stiffness: 360, damping: 34, mass: 0.72 },
} satisfies Record<string, Transition>

export const fade: Variants = {
  hidden: { opacity: 0 },
  visible: { opacity: 1, transition: motionTransition.enter },
  exit: { opacity: 0, transition: motionTransition.exit },
}

export const fadeUp: Variants = {
  hidden: { opacity: 0, y: 8 },
  visible: { opacity: 1, y: 0, transition: motionTransition.enter },
  exit: { opacity: 0, y: 4, transition: motionTransition.exit },
}

export const rowMotion: Variants = {
  hidden: { opacity: 0, y: 5 },
  visible: { opacity: 1, y: 0, transition: motionTransition.enter },
  exit: { opacity: 0, y: -3, transition: motionTransition.exit },
}

export const popoverMotion: Variants = {
  hidden: { opacity: 0, y: 7, scale: 0.985 },
  visible: { opacity: 1, y: 0, scale: 1, transition: motionSpring.surface },
  exit: { opacity: 0, y: 4, scale: 0.99, transition: motionTransition.exit },
}

export const modalMotion: Variants = {
  hidden: { opacity: 0, y: 14, scale: 0.975 },
  visible: { opacity: 1, y: 0, scale: 1, transition: motionSpring.surface },
  exit: { opacity: 0, y: 7, scale: 0.985, transition: motionTransition.exit },
}

export const backdropMotion: Variants = {
  hidden: { opacity: 0 },
  visible: { opacity: 1, transition: { duration: 0.16, ease: easeOut } },
  exit: { opacity: 0, transition: { duration: 0.12, ease: easeIn } },
}

export const controlMotion = {
  whileHover: { y: -1 },
  whileTap: { scale: 0.96 },
  transition: motionSpring.control,
} as const

export const softControlMotion = {
  whileHover: { scale: 1.015 },
  whileTap: { scale: 0.975 },
  transition: motionSpring.control,
} as const

