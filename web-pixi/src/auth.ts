// Manual op-channel wrappers for auth-service ops. Until the SDK generator
// picks up service-kind opcodes, this file holds the typed bridge between
// the auth proto schemas and the SpaceClient transport. Mirrors the pattern
// in sdk/client.ts for marketplace ops (sendMarketBrowse, etc.).

import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  AuthLoginRequestSchema,
  AuthLoginResponseSchema,
  AuthLogoutRequestSchema,
  AuthLogoutResponseSchema,
  AuthRegisterRequestSchema,
  AuthRegisterResponseSchema,
  AuthValidateTokenRequestSchema,
  AuthValidateTokenResponseSchema,
  type AuthLoginResponse,
  type AuthLogoutResponse,
  type AuthRegisterResponse,
  type AuthValidateTokenResponse,
} from "../../gen/es/enginepb/auth_pb.js";
import type { SpaceClient } from "../sdk/client.js";

const AUTH_OPCODE_LOGIN = 50;
const AUTH_OPCODE_REGISTER = 51;
const AUTH_OPCODE_VALIDATE_TOKEN = 52;
const AUTH_OPCODE_LOGOUT = 53;

export const TOKEN_KEY = "mmokit-auth-token";

export async function authLogin(
  client: SpaceClient,
  username: string,
  password: string,
): Promise<AuthLoginResponse> {
  const req = create(AuthLoginRequestSchema, { username, password });
  const respData = await client.sendOp(
    AUTH_OPCODE_LOGIN,
    toBinary(AuthLoginRequestSchema, req),
  );
  return fromBinary(AuthLoginResponseSchema, respData) as AuthLoginResponse;
}

export async function authRegister(
  client: SpaceClient,
  username: string,
  password: string,
): Promise<AuthRegisterResponse> {
  const req = create(AuthRegisterRequestSchema, { username, password });
  const respData = await client.sendOp(
    AUTH_OPCODE_REGISTER,
    toBinary(AuthRegisterRequestSchema, req),
  );
  return fromBinary(
    AuthRegisterResponseSchema,
    respData,
  ) as AuthRegisterResponse;
}

export async function authValidateToken(
  client: SpaceClient,
  token: string,
): Promise<AuthValidateTokenResponse> {
  const req = create(AuthValidateTokenRequestSchema, { sessionToken: token });
  const respData = await client.sendOp(
    AUTH_OPCODE_VALIDATE_TOKEN,
    toBinary(AuthValidateTokenRequestSchema, req),
  );
  return fromBinary(
    AuthValidateTokenResponseSchema,
    respData,
  ) as AuthValidateTokenResponse;
}

export async function authLogout(
  client: SpaceClient,
): Promise<AuthLogoutResponse> {
  const req = create(AuthLogoutRequestSchema, {});
  const respData = await client.sendOp(
    AUTH_OPCODE_LOGOUT,
    toBinary(AuthLogoutRequestSchema, req),
  );
  return fromBinary(AuthLogoutResponseSchema, respData) as AuthLogoutResponse;
}
