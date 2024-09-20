
'use strict';

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

const fs = require('node:fs');

function randomString(length) {
	const characters = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
	const charactersLength = characters.length;
	let result = '';
	let counter = 0;
	while (counter < length) {
      result += characters.charAt(Math.floor(Math.random() * charactersLength));
      counter += 1;
    }
    return result;
}

/**
 * Workload module for the benchmark round.
 */
class PutStringWorkload extends WorkloadModuleBase {
    /**
     * Initializes the workload module instance.
     */
    constructor() {
        super();
        this.txIndex = 0;
		this.dataSize = 100;
		this.numSampleData = 100;
        this.maxRate = 0;
		this.sampleData = [];
        this.transactionPromises = [];
    }
    /**
     * Initialize the workload module with the given parameters.
     * @param {number} workerIndex The 0-based index of the worker instantiating the workload module.
     * @param {number} totalWorkers The total number of workers participating in the round.
     * @param {number} roundIndex The 0-based index of the currently executing round.
     * @param {Object} roundArguments The user-provided arguments for the round from the benchmark configuration file.
     * @param {BlockchainInterface} sutAdapter The adapter of the underlying SUT.
     * @param {Object} sutContext The custom context object provided by the SUT adapter.
     * @async
     */
    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);

		this.dataSize = this.roundArguments.dataSize;
		// The numDistinctObjects argument is optional, so it may not exist in the roundArguments object
		if(this.roundArguments.hasOwnProperty('numDistinctObjects')) {
			this.numSampleData = this.roundArguments.numDistinctObjects;
		}
        // If present, the maxRate argument just records the value passed to the fixed-rate controller
        // so we can use it in the output file name
        if(this.roundArguments.hasOwnProperty('maxRate')) {
            this.maxRate = this.roundArguments.maxRate;
        }
		for(let i = 0; i < this.numSampleData; i++) {
			this.sampleData.push(randomString(this.dataSize));
		}
	}


    /**
     * Assemble TXs for the round.
     * @return {Promise<TxStatus[]>}
     */
    async submitTransaction() {
        this.txIndex++;
        let key = 'Client' + this.workerIndex + '_KEY' + this.txIndex.toString();
		let value = this.sampleData[Math.floor(Math.random() * this.sampleData.length)];

        let args = {
            contractId: 'plain_string',
            contractVersion: 'v1',
            contractFunction: 'PutString',
            contractArguments: [key, value],
            timeout: 30
        };

        let resultPromise = this.sutAdapter.sendRequests(args);
        // Save the Promise<TxStatus> from sendRequests before returning it
        this.transactionPromises.push(resultPromise);
        return resultPromise;
    }

    /**
     * Called once at the end of the round.
     * Saves transaction completion timestamps to a file.
     */
    async cleanupWorkloadModule() {
        // Only include the message rate in the file name if it was provided
        let fileName;
        if(this.maxRate > 0) {
            fileName = `timestamp-${this.dataSize}-r${this.maxRate}_w${this.workerIndex}.log`;
        } else {
            fileName = `timestamp-${this.dataSize}_w${this.workerIndex}.log`;
        }
        let fileStream = fs.createWriteStream(fileName, {flags: 'a'});
        for (const txPromise of this.transactionPromises) {
            // By this time all of the transactions should have completed, so the promise should be available right away
            let transactionStatus = await txPromise;
            fileStream.write(`${transactionStatus.GetID()} ${transactionStatus.GetTimeCreate()} ${transactionStatus.GetTimeFinal()}\n`);
        }
        fileStream.end();
    }
}

/**
 * Create a new instance of the workload module.
 * @return {WorkloadModuleInterface}
 */
function createWorkloadModule() {
    return new PutStringWorkload();
}

module.exports.createWorkloadModule = createWorkloadModule;
