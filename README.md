# Bzero

## Bastionzero

Bastionzero is a simple to use zero trust access SaaS for dynamic cloud environments. Bastionzero is the most secure way to lock down remote access to servers, containers, clusters, and VM’s in any cloud, public or private. For more information go to [Bastionzero](https://www.bastionzero.com).

The bzero-agent and bzero-daemon are executables that run on your local machine and target to communicate with the Bastionzero SaaS. 

## Install
We bundle our daemon with our cli tool `zli`: 
```
brew tap bastionzero/tap
brew install bastionzero/tap/zli
```

To install the Agent, you can quickly get started by looking at our [helm charts](https://github.com/bastionzero/charts).

## Developer processes

We use go to run and test our code. You can build our agent or daemon using the following command for our agent:
```
cd bctl/agent && go build agent.go
```

And this command for our daemon:
```
cd bctl/daemon && go build daemon.go
```

You can then run the agent and daemon by running the executable.

Where {version} is the version that is defined in the `package.json` file. This means older versions are still accessible but the `latest` folder will always overwritten by the codebuild job.
