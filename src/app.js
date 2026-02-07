// website
import Fastify from "fastify";
import cors from "@fastify/cors";

// font
import { initCheck } from "./bootstrap/init.js";
import dotenv from "dotenv";

// routes
import registerPages from "./website/pages.js";
import { registerApi } from "./website/api.js";
import registerStatic, { generateEmfontJS } from "./website/static.js";

dotenv.config();
const state = {
    alive: false,
    bulletin: process.env.BULLETIN || "",
    local: true,
    r2: false
}; //預設很保守，預設都是關閉，會在init過程中打開
const port = process.env.PORT ?? 3000;
state.baseURL = process.env.BASE_URL ?? `http://localhost:${port}`;
if (process.env.MINIO_redirect == "true") state.static_font_base = state.baseURL + "/file/_generated";
else state.static_font_base = "_generated";
state.REGEN_STATIC = process.env.REGEN_STATIC === "true";
state.REGEN_CSS = process.env.REGEN_CSS === "true";
state.R2_PUB_URL_BASE = process.env.R2_PUB_URL_BASE ?? "";
state.FONT_CHECK = process.env.FONT_CHECK === "true";

const envToLogger = {
  development: {
    transport: {
      target: 'pino-pretty',
      options: {
        translateTime: 'HH:MM:ss Z',
        ignore: 'pid,hostname',
      },
    },
  },
  production: true,
  test: false,
}
const app = Fastify({
    //todo: 把 logger 改成環境變數控制
  logger: envToLogger['development'] ?? true // defaults to true if no entry matches in the map
})
// const app = Fastify({ logger: { level: "info" ,prettyPrint: true,}, ignoreTrailingSlash: true });

app.register(cors, {
    origin: "*",
    methods: ["GET", "POST"],
    allowedHeaders: ["Content-Type", "Authorization"],
    credentials: true
});

await registerPages(app);
await registerApi(app, state);
await registerStatic(app);

// Start server
const start = async () => {
    try {
        app.listen({ port: port, host: "0.0.0.0" }, () => {
            app.log.info(`🔗 網頁啟動在 ${state.baseURL}`, { service: "emfont", phase: "startup" });
        });
    } catch (err) {
        app.log.error(err);
        process.exit(1);
    }
};

start();

//init
app.ready().then(async () => {
    await initCheck(state, app.log);
    await generateEmfontJS(state);

    if (state.alive) {
        app.log.info("🎉 初始化成功，服務已啟動");
    } else {
        app.log.error("🤨 初始化失敗，網頁仍在運行");
        if (!state.bulletin) state.bulletin += "<br>😭emfont 啟動失敗，暫時無法使用。<br>";
    }
});
