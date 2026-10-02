import { createServer } from 'vite';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('.', import.meta.url));
const studio = fileURLToPath(new URL('..', import.meta.url));
const marks=[
 ['App.tsx','  const workspace = useWorkspace();','App'],
 ['App.tsx','  const active = useRef(true);','AccountWorkspace'],
 ['App.tsx','  const events = useRosterEvents(journal);','RosterRegion'],
 ['App.tsx','  const events = useBotEvents(journal, props.bot.id);','ConversationPane'],
 ['Transcript.tsx','  return <div className="transcript-markdown">','Markdown'],
 ['Transcript.tsx','  const segments = buildTurnSegments(turn);','Turn'],
 ['ChatRoom.tsx','  const scroll = useRef','ChatRoom'],
 ['Composer.tsx','  const textarea = useRef','Composer'],
 ['BotIsland.tsx','  const dialog = useRef','BotIsland'],
 ['BotRoster.tsx','  const { bot, node } = entry;','RosterRow'],
];
const server=await createServer({configFile:false,root,plugins:[{name:'render-counters',enforce:'pre',transform(code,id){for(const [file,anchor,name] of marks)if(id.endsWith('/'+file)) code=code.replace(anchor,`globalThis.__counts ??= {}; globalThis.__counts['${name}']=(globalThis.__counts['${name}']||0)+1;\n${anchor}`);return code;}}],esbuild:{jsx:'automatic'},server:{host:'127.0.0.1',port:5187,strictPort:true,fs:{allow:[studio]}}}); await server.listen(); console.log('Perf fixture on 5187');
