// /** @format */

// import { drizzle } from "drizzle-orm/node-postgres";
// import pkg from "pg";
// const { Pool, Client } = pkg;
// import * as schema from "./schema.js";
// import "dotenv/config";

// const pool = new Pool({
//     connectionString: process.env.DATABASE_URL,
// });

// export const db = drizzle(pool, { schema });

// // Create tables if they don't exist
import pg from "pg";
import dotenv from "dotenv";
dotenv.config();
const { Client } = pg;

const db = new Client({
    connectionString: process.env.DATABASE_URL, 
});

db.connect()
    .then(() => console.log("Connected to PostgreSQL"))
    .catch(err => console.error("Connection error", err.stack));

export { db };
